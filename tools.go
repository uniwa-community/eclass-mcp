package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// instructions is sent to the client on connect and teaches the model how to
// move around eClass with a handful of generic tools.
const instructions = `This server gives you the logged in user's Open eClass account.

How to work with it:
1. eclass_my_courses: the user's name, enrolled courses (with course codes and, for student enrollments, the numeric course id some pages such as main/unregcours.php?cid= need) and personal tools (messages, calendar, announcements from all courses, notes, profile, stats).
2. eclass_course_tools: every module enabled in one course (announcements, documents, assignments, exercises, forum, gradebook and so on) with its URL.
3. eclass_get_page: open any eClass URL. You get markdown with absolute links, every form with its fields, and any AJAX data endpoints. Follow links to go deeper.
4. eclass_submit_form: perform an action (submit an assignment, reply in a forum, send a message, answer an exercise, enroll in a course). Read the page with the form first and send all of its fields, hidden ones included, because eClass checks a "token" field.
5. eclass_download: save a document or attachment to disk.

Useful shortcuts: main/personal_calendar/icalendar.php returns every event and deadline of all courses as iCal text, and modules/announcements/myannouncements.php lists announcements from all courses.

Pages are usually in Greek. Some lists (announcements, messages and others) are filled by JavaScript. The page then lists its "Data endpoints" with their HTTP method; call them with ajax=true (eclass_get_page for GET, eclass_submit_form for POST). For DataTables send draw=1, start=0, length=100.
eClass uses a single CSRF token per session: pages show it as "CSRF token for this session", and any POST (forms or data endpoints) must include it as token.

Enrolling and unregistering: the course catalogue (modules/auth/courses.php) uses JavaScript that posts to modules/auth/course_submit.php with cid=<numeric course id>, state=true to enroll or state=false to unregister, token=<CSRF token>, plus password=<course password> if the course has one. Send it with ajax=true. It answers registered, unregistered, unauthorized or prereqsnotcomplete. Unregistering may be impossible to undo (closed courses do not accept new students), so always warn the user and ask before unregistering.

Ask the user before submitting anything that cannot be undone, such as a final exercise submission or a deletion. Plain links can trigger actions too (links containing delete, unregister, logout, or settings toggles such as email_un), so do not open those without being asked.`

const defaultMaxChars = 40000

// pageWindow is embedded in tools that return long pages.
type pageWindow struct {
	AJAX     bool `json:"ajax,omitempty" jsonschema:"set to true when calling one of a page's data endpoints so eClass answers with JSON"`
	Offset   int  `json:"offset,omitempty" jsonschema:"character offset to continue a long page from (default 0)"`
	MaxChars int  `json:"max_chars,omitempty" jsonschema:"maximum characters to return (default 40000)"`
}

type courseInput struct {
	Course string `json:"course" jsonschema:"course code such as CS101, as listed by eclass_my_courses"`
}

type getPageInput struct {
	URL string `json:"url" jsonschema:"eClass URL, absolute or relative to the eClass root (e.g. modules/announcements/index.php?course=CS101)"`
	pageWindow
}

type submitFormInput struct {
	URL    string  `json:"url" jsonschema:"form action URL as shown by eclass_get_page"`
	Method string  `json:"method,omitempty" jsonschema:"GET or POST (default POST)"`
	Fields []Field `json:"fields,omitempty" jsonschema:"form fields to send, include hidden ones like token"`
	Files  []Field `json:"files,omitempty" jsonschema:"file uploads: name is the form field, value is an absolute local file path"`
	pageWindow
}

type downloadInput struct {
	URL string `json:"url" jsonschema:"eClass URL of the file to download"`
}

func addTools(s *mcp.Server, c *Client, downloadDir string) {
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "eclass_my_courses",
		Description: "Show the logged in user's name, enrolled courses with their codes, and personal tools (messages, calendar, announcements, notes, profile).",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		var p struct {
			Groups []struct {
				Name    string `xml:"name,attr"`
				Courses []struct {
					Code       string `xml:"code,attr"`
					PublicCode string `xml:"public_code,attr"`
					Title      string `xml:"title,attr"`
					Teacher    string `xml:"teacher,attr"`
				} `xml:"course"`
			} `xml:"courses>coursegroup"`
			Tools   []xmlTool `xml:"tools>tool"`
			Profile []xmlTool `xml:"profiletools>tool"`
			User    struct {
				Given   string `xml:"givenname,attr"`
				Surname string `xml:"surname,attr"`
			} `xml:"user"`
		}
		if err := c.mobileXML(ctx, "modules/mobile/mportfolio.php", nil, &p); err != nil {
			return nil, nil, err
		}

		ids := c.courseIDs(ctx)

		var b strings.Builder
		fmt.Fprintf(&b, "User: %s %s\n", p.User.Given, p.User.Surname)
		for _, g := range p.Groups {
			fmt.Fprintf(&b, "\n## %s\n\n", g.Name)
			for _, co := range g.Courses {
				id := ""
				if ids[co.Code] != "" {
					id = ", course id " + ids[co.Code]
				}
				fmt.Fprintf(&b, "- %s (course code `%s`%s, public code %s), %s\n", co.Title, co.Code, id, co.PublicCode, co.Teacher)
			}
		}
		b.WriteString("\n## Personal tools\n\n")
		for _, t := range append(p.Profile, p.Tools...) {
			fmt.Fprintf(&b, "- %s: %s\n", t.Name, cleanToolURL(t.Redirect))
		}
		return text(b.String()), nil, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "eclass_course_tools",
		Description: "List every module of a course (announcements, documents, assignments, exercises, forum, gradebook, etc.) with the URL to open it.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in courseInput) (*mcp.CallToolResult, any, error) {
		var t struct {
			Groups []struct {
				Name  string    `xml:"name,attr"`
				Tools []xmlTool `xml:"tool"`
			} `xml:"toolgroup"`
			Status struct {
				Name string `xml:"name,attr"`
			} `xml:"status"`
		}
		if err := c.mobileXML(ctx, "modules/mobile/mtools.php", []Field{{Name: "course", Value: in.Course}}, &t); err != nil {
			return nil, nil, err
		}

		var b strings.Builder
		fmt.Fprintf(&b, "Course %s, you are a %s.\nCourse home: %s\n", in.Course, t.Status.Name, c.base.JoinPath("courses", in.Course).String()+"/")
		for _, g := range t.Groups {
			fmt.Fprintf(&b, "\n## %s\n\n", g.Name)
			for _, tool := range g.Tools {
				state := ""
				if tool.Active != "1" && tool.Active != "true" {
					state = " (disabled for students)"
				}
				fmt.Fprintf(&b, "- %s%s: %s\n", tool.Name, state, cleanToolURL(tool.Redirect))
			}
		}
		return text(b.String()), nil, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "eclass_get_page",
		Description: "Open any eClass page and get it as markdown, with absolute links, all forms and their fields, and AJAX data endpoints. JSON and XML responses are returned as is.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getPageInput) (*mcp.CallToolResult, any, error) {
		resp, err := c.Send(ctx, http.MethodGet, in.URL, nil, nil, in.AJAX)
		if err != nil {
			return nil, nil, err
		}
		out, err := describe(resp)
		if err != nil {
			return nil, nil, err
		}
		return text(window(out, in.pageWindow)), nil, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "eclass_submit_form",
		Description: "Submit an eClass form (assignment upload, forum post, message, exercise answer, poll vote, course enrollment, profile change, and so on). Read the page with eclass_get_page first and send all of the form's fields including hidden ones. Returns the resulting page.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: new(true)},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in submitFormInput) (*mcp.CallToolResult, any, error) {
		method := in.Method
		if method == "" {
			method = http.MethodPost
		}
		resp, err := c.Send(ctx, method, in.URL, in.Fields, in.Files, in.AJAX)
		if err != nil {
			return nil, nil, err
		}
		out, err := describe(resp)
		if err != nil {
			return nil, nil, err
		}
		return text(window(out, in.pageWindow)), nil, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "eclass_download",
		Description: "Download a file from eClass (document, assignment attachment, folder zip, video) into " + downloadDir + " and return the saved path.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in downloadInput) (*mcp.CallToolResult, any, error) {
		resp, err := c.Send(ctx, http.MethodGet, in.URL, nil, nil, false)
		if err != nil {
			return nil, nil, err
		}
		defer resp.Body.Close()
		if strings.Contains(resp.Header.Get("Content-Type"), "html") {
			page, _ := describe(resp)
			return nil, nil, fmt.Errorf("got a web page instead of a file, maybe the link needs another step:\n\n%s", window(page, pageWindow{MaxChars: 4000}))
		}
		if resp.StatusCode >= 400 {
			return nil, nil, fmt.Errorf("download failed: HTTP %s", resp.Status)
		}

		if err := os.MkdirAll(downloadDir, 0o755); err != nil {
			return nil, nil, err
		}
		dest := filepath.Join(downloadDir, fileName(resp))
		f, err := os.Create(dest)
		if err != nil {
			return nil, nil, err
		}
		n, err := io.Copy(f, resp.Body)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return nil, nil, fmt.Errorf("saving %s: %w", dest, err)
		}
		return text(fmt.Sprintf("Saved %d bytes (%s) to %s", n, resp.Header.Get("Content-Type"), dest)), nil, nil
	})
}

// xmlTool is a tool entry from the mobile API.
type xmlTool struct {
	Name     string `xml:"name,attr"`
	Redirect string `xml:"redirect,attr"`
	Active   string `xml:"active,attr"`
}

// courseLinkOrID matches, in page order, either a link into a course
// ("courses/CODE/") or the numeric id in an unregister link ("cid=123").
var courseLinkOrID = regexp.MustCompile(`courses/([^/'"\s]+)/|unregcours\.php\?[^'"]*?cid=(\d+)`)

// courseIDs maps course codes to eClass's numeric course ids. A few pages
// (unregistering from a course, for one) want the id instead of the code,
// and the mobile API never exposes it. main/my_courses.php does: every course
// card links to courses/CODE/ and is followed by its unregister form with
// cid=ID, so pairing each id with the course link before it is enough.
// This is best effort; on any error the list simply comes without ids.
func (c *Client) courseIDs(ctx context.Context) map[string]string {
	ids := map[string]string{}
	resp, err := c.Send(ctx, http.MethodGet, "main/my_courses.php", nil, nil, false)
	if err != nil {
		return ids
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ids
	}
	last := ""
	for _, m := range courseLinkOrID.FindAllStringSubmatch(string(body), -1) {
		switch {
		case m[1] != "":
			last = m[1]
		case last != "":
			ids[last] = m[2]
			last = ""
		}
	}
	return ids
}

// mobileXML calls a mobile API endpoint and decodes its XML answer.
func (c *Client) mobileXML(ctx context.Context, endpoint string, fields []Field, v any) error {
	resp, err := c.Send(ctx, http.MethodPost, endpoint, fields, nil, false)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if s := strings.TrimSpace(string(body)); s == "FAILED" || s == "NOTENABLED" {
		return fmt.Errorf("eClass refused %s (%s); check the course code", endpoint, s)
	}
	return xml.Unmarshal(body, v)
}

// cleanToolURL drops the "view=mobile" flag the mobile API appends to links,
// once raw and once url-encoded, so the model sees normal eClass URLs.
func cleanToolURL(u string) string {
	for _, suffix := range []string{"%26view%3Dmobile", "&view=mobile", "?view=mobile"} {
		u = strings.TrimSuffix(u, suffix)
	}
	return u
}

// describe turns any eClass response into text for the model.
func describe(resp *http.Response) (string, error) {
	defer resp.Body.Close()
	ct := resp.Header.Get("Content-Type")
	prefix := ""
	if resp.StatusCode >= 400 {
		prefix = fmt.Sprintf("HTTP %s\n\n", resp.Status)
	}
	if !isText(ct) {
		return prefix + fmt.Sprintf("This URL is a file (%s, %d bytes). Use eclass_download to save it.", ct, resp.ContentLength), nil
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	// eClass often sends JSON with a text/html content type, so sniff it.
	if trimmed := bytes.TrimSpace(body); len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') && json.Valid(trimmed) {
		out, err := readableJSON(trimmed, resp.Request.URL)
		return prefix + out, err
	}
	if ct == "" || strings.Contains(ct, "html") {
		out, err := renderPage(resp.Request.URL, bytes.NewReader(body))
		return prefix + out, err
	}
	return prefix + string(body), nil
}

// readableJSON pretty prints JSON and turns the HTML snippets that DataTables
// endpoints put in their cells into markdown, links included.
func readableJSON(data []byte, base *url.URL) (string, error) {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return "", err
	}
	var walk func(any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case string:
			if strings.Contains(x, "<") {
				if md, err := fragmentToMarkdown(base, x); err == nil {
					return md
				}
			}
		case []any:
			for i := range x {
				x[i] = walk(x[i])
			}
		case map[string]any:
			for k := range x {
				x[k] = walk(x[k])
			}
		}
		return v
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	err := enc.Encode(walk(v))
	return buf.String(), err
}

// window returns the requested slice of a long text plus a note on how to get
// the rest. It counts runes so Greek text is never cut mid character.
func window(s string, w pageWindow) string {
	limit := w.MaxChars
	if limit <= 0 {
		limit = defaultMaxChars
	}
	r := []rune(s)
	start := min(max(w.Offset, 0), len(r))
	end := min(start+limit, len(r))
	out := string(r[start:end])
	if start > 0 || end < len(r) {
		out += fmt.Sprintf("\n\n[showing characters %d to %d of %d", start, end, len(r))
		if end < len(r) {
			out += fmt.Sprintf(", call again with offset=%d for more", end)
		}
		out += "]"
	}
	return out
}

// fileName picks a safe local file name from Content-Disposition, falling back
// to the last part of the URL.
func fileName(resp *http.Response) string {
	name := ""
	if _, params, err := mime.ParseMediaType(resp.Header.Get("Content-Disposition")); err == nil {
		name = params["filename"]
	}
	if name == "" {
		name = path.Base(resp.Request.URL.Path)
	}
	name = filepath.Base(strings.ReplaceAll(name, `\`, "/"))
	if name == "." || name == "/" || name == ".." || name == "" {
		name = "download"
	}
	return name
}

func text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}
