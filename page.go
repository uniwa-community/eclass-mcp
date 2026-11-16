package main

import (
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"

	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/base"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/commonmark"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/table"
	"github.com/PuerkitoBio/goquery"
)

var markdown = converter.NewConverter(converter.WithPlugins(
	base.NewBasePlugin(),
	commonmark.NewCommonmarkPlugin(),
	table.NewTablePlugin(),
))

// Many eClass lists (messages, some tables) are filled in by DataTables or
// jQuery AJAX calls. We pull those URLs out of inline scripts so the model can
// fetch the JSON directly.
var ajaxURL = regexp.MustCompile(`(?:['"]?url['"]?|sAjaxSource)\s*:\s*['"]([^'"]+)['"]`)

var blankLines = regexp.MustCompile(`\n{3,}`)

// renderPage reduces an eClass HTML page to markdown: the title, the main
// content, every form with its fields, and any AJAX data endpoints. All links
// come out absolute so they can be passed straight back to the tools.
func renderPage(pageURL *url.URL, body io.Reader) (string, error) {
	doc, err := goquery.NewDocumentFromReader(body)
	if err != nil {
		return "", err
	}

	abs := func(ref string) string {
		u, err := pageURL.Parse(strings.TrimSpace(ref))
		if err != nil {
			return ref
		}
		return u.String()
	}

	var endpoints []string
	seen := map[string]bool{}
	doc.Find("script").Each(func(_ int, s *goquery.Selection) {
		for _, m := range ajaxURL.FindAllStringSubmatch(s.Text(), -1) {
			if strings.HasPrefix(m[1], "javascript:") || strings.Contains(m[1], "'+") {
				continue
			}
			if u := abs(m[1]); !seen[u] {
				seen[u] = true
				endpoints = append(endpoints, u)
			}
		}
	})

	root := doc.Find("main").First()
	if root.Length() == 0 {
		root = doc.Find("body")
	}
	root.Find("script, style, noscript, svg, .offcanvas").Remove()

	root.Find("[href]").Each(func(_ int, s *goquery.Selection) {
		href, _ := s.Attr("href")
		if strings.HasPrefix(strings.TrimSpace(href), "javascript:") || strings.HasPrefix(href, "#") {
			s.RemoveAttr("href")
			return
		}
		s.SetAttr("href", abs(href))
	})
	root.Find("[src]").Each(func(_ int, s *goquery.Selection) {
		src, _ := s.Attr("src")
		s.SetAttr("src", abs(src))
	})

	var out strings.Builder
	if title := strings.TrimSpace(doc.Find("title").First().Text()); title != "" {
		fmt.Fprintf(&out, "# %s\n\n", title)
	}
	fmt.Fprintf(&out, "URL: %s\n\n", pageURL)

	html, err := goquery.OuterHtml(root)
	if err != nil {
		return "", err
	}
	md, err := markdown.ConvertString(html)
	if err != nil {
		return "", err
	}
	out.WriteString(strings.TrimSpace(blankLines.ReplaceAllString(md, "\n\n")))
	out.WriteString("\n")

	root.Find("form").Each(func(i int, f *goquery.Selection) {
		writeForm(&out, i+1, f, pageURL, abs)
	})

	if len(endpoints) > 0 {
		out.WriteString("\n## Data endpoints used by this page\n\n")
		out.WriteString("These return JSON. DataTables endpoints usually want draw=1&start=0&length=100.\n\n")
		for _, e := range endpoints {
			fmt.Fprintf(&out, "- %s\n", e)
		}
	}
	return out.String(), nil
}

// writeForm prints one form in a compact, copy friendly way: where it posts,
// and every field with its current value. Hidden fields matter most, since
// eClass protects actions with a CSRF "token" field.
func writeForm(out *strings.Builder, n int, f *goquery.Selection, pageURL *url.URL, abs func(string) string) {
	action := pageURL.String()
	if a, ok := f.Attr("action"); ok && strings.TrimSpace(a) != "" {
		action = abs(a)
	}
	method := strings.ToUpper(f.AttrOr("method", "GET"))
	fmt.Fprintf(out, "\n## Form %d: %s %s", n, method, action)
	if enc := f.AttrOr("enctype", ""); strings.Contains(enc, "multipart") {
		out.WriteString(" (accepts file uploads)")
	}
	out.WriteString("\n\n")

	f.Find("input, select, textarea, button").Each(func(_ int, in *goquery.Selection) {
		name, ok := in.Attr("name")
		if !ok || name == "" {
			return
		}
		tag := goquery.NodeName(in)
		kind := tag
		if tag == "input" {
			kind = strings.ToLower(in.AttrOr("type", "text"))
		}
		value := in.AttrOr("value", "")

		switch tag {
		case "textarea":
			value = in.Text()
		case "select":
			var opts []string
			in.Find("option").Each(func(_ int, o *goquery.Selection) {
				v := o.AttrOr("value", strings.TrimSpace(o.Text()))
				opt := fmt.Sprintf("%q=%s", v, strings.TrimSpace(o.Text()))
				if _, sel := o.Attr("selected"); sel {
					opt += " [selected]"
				}
				opts = append(opts, opt)
			})
			fmt.Fprintf(out, "- `%s` (select): %s\n", name, strings.Join(opts, ", "))
			return
		}

		line := fmt.Sprintf("- `%s` (%s) = %q", name, kind, value)
		if _, checked := in.Attr("checked"); checked {
			line += " [checked]"
		}
		if label := fieldLabel(in); label != "" {
			line += " " + label
		}
		out.WriteString(line + "\n")
	})
}

// fieldLabel finds the human label of a field, which is what tells the model
// that radio "3" means "Paris" in a quiz.
func fieldLabel(in *goquery.Selection) string {
	if id, ok := in.Attr("id"); ok && id != "" {
		if l := in.Closest("form").Find(`label[for="` + id + `"]`); l.Length() > 0 {
			return "label: " + strings.Join(strings.Fields(l.First().Text()), " ")
		}
	}
	if l := in.Closest("label"); l.Length() > 0 {
		if t := strings.Join(strings.Fields(l.Text()), " "); t != "" {
			return "label: " + t
		}
	}
	return ""
}
