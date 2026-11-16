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
	"golang.org/x/net/html"
)

var markdown = converter.NewConverter(converter.WithPlugins(
	base.NewBasePlugin(),
	commonmark.NewCommonmarkPlugin(),
	table.NewTablePlugin(),
))

// Many eClass lists (messages, some tables) are filled in by DataTables or
// jQuery AJAX calls. We pull those URLs out of inline scripts so the model can
// fetch the JSON directly.
var (
	ajaxURL = regexp.MustCompile(`(?:['"]?url['"]?|sAjaxSource)\s*:\s*['"]([^'"]+)['"]`)
	// Uppy, the upload widget eClass 4.x uses for assignments, adds its file
	// field from JavaScript, so it never shows up as a form input.
	uploadField = regexp.MustCompile(`fieldName\s*:\s*['"]([^'"]+)['"]`)
	ajaxType    = regexp.MustCompile(`^[^}]*?['"]?(?:type|method)['"]?\s*:\s*['"](\w+)['"]`)
)

var blankLines = regexp.MustCompile(`\n{3,}`)

// renderPage reduces an eClass HTML page to markdown: the title, the main
// content, every form with its fields, and any AJAX data endpoints. All links
// come out absolute so they can be passed straight back to the tools.
func renderPage(pageURL *url.URL, body io.Reader) (string, error) {
	doc, err := goquery.NewDocumentFromReader(body)
	if err != nil {
		return "", err
	}

	abs := resolver(pageURL)

	var endpoints, uploads []string
	seen := map[string]bool{}
	doc.Find("script").Each(func(_ int, s *goquery.Selection) {
		js := s.Text()
		for _, m := range uploadField.FindAllStringSubmatch(js, -1) {
			uploads = append(uploads, m[1])
		}
		for _, m := range ajaxURL.FindAllStringSubmatchIndex(js, -1) {
			ref := js[m[2]:m[3]]
			if strings.HasPrefix(ref, "javascript:") || strings.Contains(ref, "'+") {
				continue
			}
			// The HTTP method usually sits right next to the url in the
			// same object literal; jQuery defaults to GET otherwise.
			method := "GET"
			if t := ajaxType.FindStringSubmatch(js[m[1]:min(m[1]+200, len(js))]); t != nil {
				method = strings.ToUpper(t[1])
			}
			if e := method + " " + abs(ref); !seen[e] {
				seen[e] = true
				endpoints = append(endpoints, e)
			}
		}
	})

	root := doc.Find("main").First()
	if root.Length() == 0 {
		root = doc.Find("body")
	}
	var out strings.Builder
	if title := strings.TrimSpace(doc.Find("title").First().Text()); title != "" {
		fmt.Fprintf(&out, "# %s\n\n", title)
	}
	fmt.Fprintf(&out, "URL: %s\n\n", pageURL)

	md, err := toMarkdown(root, abs)
	if err != nil {
		return "", err
	}
	out.WriteString(md)
	out.WriteString("\n")

	root.Find("form").Each(func(i int, f *goquery.Selection) {
		writeForm(&out, i+1, f, pageURL, abs)
	})

	// Some pages, like the course catalogue, keep their inputs outside any
	// form and let JavaScript post them; list those too.
	orphans := root.Find("input, select, textarea").FilterFunction(func(_ int, s *goquery.Selection) bool {
		return s.Closest("form").Length() == 0
	})
	if orphans.Length() > 0 {
		out.WriteString("\n## Fields outside any form\n\nJavaScript on this page sends these itself.\n\n")
		writeFields(&out, orphans)
	}

	// eClass uses one CSRF token per session. It often only appears in the
	// header forms, which are cut above, but JavaScript endpoints need it too.
	if tok := doc.Find("input[name=token]").First().AttrOr("value", ""); tok != "" {
		fmt.Fprintf(&out, "\nCSRF token for this session: `%s`. Send it as `token` with any POST, including to data endpoints.\n", tok)
	}

	for _, name := range uploads {
		fmt.Fprintf(&out, "\nFile upload field added by JavaScript: `%s`. Send it with eclass_submit_form files, together with the fields of the form above.\n", name)
	}

	if len(endpoints) > 0 {
		out.WriteString("\n## Data endpoints used by this page\n\n")
		out.WriteString("Call these with ajax=true to get JSON. DataTables endpoints want draw=1, start=0, length=100.\n\n")
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
	writeFields(out, f.Find("input, select, textarea, button"))
}

// writeFields prints one line per named field with its type, current value
// and label.
func writeFields(out *strings.Builder, fields *goquery.Selection) {
	fields.Each(func(_ int, in *goquery.Selection) {
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

// iconLabel finds the accessible name of an icon only link.
func iconLabel(a *goquery.Selection) string {
	for _, sel := range []*goquery.Selection{a, a.Find("[aria-label], [title], [data-bs-original-title]")} {
		for _, attr := range []string{"data-bs-original-title", "title", "aria-label"} {
			if v := strings.TrimSpace(sel.AttrOr(attr, "")); v != "" {
				return v
			}
		}
	}
	return ""
}

// removeComments drops HTML comments, which the markdown converter would
// otherwise print verbatim.
func removeComments(nodes ...*html.Node) {
	for _, n := range nodes {
		for c := n.FirstChild; c != nil; {
			next := c.NextSibling
			if c.Type == html.CommentNode {
				n.RemoveChild(c)
			} else {
				removeComments(c)
			}
			c = next
		}
	}
}

// resolver returns a function that makes links absolute relative to base.
func resolver(base *url.URL) func(string) string {
	return func(ref string) string {
		u, err := base.Parse(strings.TrimSpace(ref))
		if err != nil {
			return ref
		}
		return u.String()
	}
}

// toMarkdown cleans a chunk of eClass HTML (scripts, comments, javascript:
// links, icon only links) and converts it to markdown with absolute links.
// It edits the selection in place.
func toMarkdown(root *goquery.Selection, abs func(string) string) (string, error) {
	root.Find("script, style, noscript, svg, .offcanvas").Remove()
	removeComments(root.Nodes...)

	root.Find("[href]").Each(func(_ int, s *goquery.Selection) {
		href, _ := s.Attr("href")
		if strings.HasPrefix(strings.TrimSpace(href), "javascript:") || strings.HasPrefix(href, "#") {
			s.RemoveAttr("href")
			return
		}
		s.SetAttr("href", abs(href))
		// Icon only links (download, edit, ...) keep their meaning in a
		// tooltip or aria-label; promote it to visible text.
		if strings.TrimSpace(s.Text()) == "" {
			if label := iconLabel(s); label != "" {
				s.SetText(label)
			}
		}
	})
	root.Find("[src]").Each(func(_ int, s *goquery.Selection) {
		src, _ := s.Attr("src")
		s.SetAttr("src", abs(src))
	})

	markup, err := goquery.OuterHtml(root)
	if err != nil {
		return "", err
	}
	md, err := markdown.ConvertString(markup)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(blankLines.ReplaceAllString(md, "\n\n")), nil
}

// fragmentToMarkdown converts an HTML snippet, such as a DataTables cell,
// resolving its links against the URL it came from.
func fragmentToMarkdown(base *url.URL, fragment string) (string, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(fragment))
	if err != nil {
		return "", err
	}
	return toMarkdown(doc.Find("body"), resolver(base))
}
