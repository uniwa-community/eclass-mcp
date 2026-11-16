package main

import (
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeEClass serves the handful of endpoints the tools touch, with answers
// shaped like a real eClass 4.4 (see modules/mobile/*.php upstream).
func fakeEClass(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/modules/mobile/mlogin.php":
			io.WriteString(w, "tok123")
		case "/modules/mobile/mportfolio.php":
			if r.FormValue("token") != "tok123" {
				io.WriteString(w, "EXPIRED")
				return
			}
			io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?>
<portfolio>
  <courses><coursegroup name="Τα μαθήματα που παρακολουθώ">
    <course code="ICE251" public_code="ICE1-7106" title="ΕΞΟΡΥΞΗ ΔΕΔΟΜΕΝΩΝ" teacher="Α. Καθηγητής" description="" image=""/>
  </coursegroup></courses>
  <tools><tool name="Εγγραφή σε μάθημα" redirect="https://x/modules/auth/courses.php?view=mobile" type="coursesubscribe" active="true"/></tools>
  <profiletools><tool name="Τα μηνύματά μου" redirect="https://x/modules/message/index.php?view=mobile" type="mymessages" active="true"/></profiletools>
  <user givenname="ΓΙΑΝΝΗΣ" surname="ΦΟΙΤΗΤΗΣ"/>
</portfolio>`)
		case "/main/my_courses.php":
			// Same shape as main/my_courses.php upstream: course card, then
			// its unregister modal. The theme logo must not confuse pairing.
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			io.WriteString(w, `<link rel="icon" href="/courses/theme_data/47/logo.png">
<a class='TextBold my-2' href='https://x/courses/ICE251/'>ΕΞΟΡΥΞΗ</a>
<img src='https://x/courses/ICE251/image/a.png'>
<form method='post' action='/main/unregcours.php?u=9&amp;cid=3251'></form>
<a class='TextBold my-2' href='https://x/courses/TEACH1/'>Taught course, no unregister form</a>`)
		case "/modules/mobile/mtools.php":
			if r.FormValue("course") != "ICE251" {
				io.WriteString(w, "FAILED")
				return
			}
			io.WriteString(w, `<tools>
  <toolgroup name="Επιλογές Μαθήματος">
    <tool name="Έγγραφα" redirect="https://x/modules/document/index.php?course=ICE251%26view%3Dmobile" active="1"/>
    <tool name="Wiki" redirect="https://x/modules/wiki/index.php?course=ICE251%26view%3Dmobile" active=""/>
  </toolgroup>
  <status name="student"/>
</tools>`)
		case "/modules/document/file.php/ICE251/slides.pdf":
			w.Header().Set("Content-Type", "application/pdf")
			w.Header().Set("Content-Disposition", `attachment; filename*=UTF-8''%CE%94%CE%B9%CE%AC%CE%BB%CE%B5%CE%BE%CE%B7.pdf`)
			io.WriteString(w, "%PDF-1.4 fake")
		case "/modules/announcements/index.php":
			if r.Header.Get("X-Requested-With") != "XMLHttpRequest" {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				io.WriteString(w, "<main><table><tr><th>Ανακοίνωση</th></tr></table></main>")
				return
			}
			io.WriteString(w, `{"recordsTotal":1,"aaData":[["<a href='index.php?an_id=5'>Νέο υλικό</a>","1/1/26"]]}`)
		}
	}))
}

func TestTools(t *testing.T) {
	srv := fakeEClass(t)
	defer srv.Close()
	c, _ := NewClient(srv.URL, "u", "p")
	dir := t.TempDir()

	server := mcp.NewServer(&mcp.Implementation{Name: "eclass-mcp"}, &mcp.ServerOptions{Instructions: instructions})
	addTools(server, c, dir)
	ctx := context.Background()
	ct, st := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	call := func(name string, args map[string]any) (string, bool) {
		t.Helper()
		res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var b strings.Builder
		for _, c := range res.Content {
			b.WriteString(c.(*mcp.TextContent).Text)
		}
		return b.String(), res.IsError
	}

	tests := []struct {
		name    string
		args    map[string]any
		want    []string
		isError bool
	}{
		{"eclass_my_courses", nil, []string{
			"User: ΓΙΑΝΝΗΣ ΦΟΙΤΗΤΗΣ",
			"ΕΞΟΡΥΞΗ ΔΕΔΟΜΕΝΩΝ (course code `ICE251`, course id 3251, public code ICE1-7106), Α. Καθηγητής",
			"Τα μηνύματά μου: https://x/modules/message/index.php\n",
			"Εγγραφή σε μάθημα: https://x/modules/auth/courses.php\n",
		}, false},
		{"eclass_course_tools", map[string]any{"course": "ICE251"}, []string{
			"you are a student",
			"Έγγραφα: https://x/modules/document/index.php?course=ICE251\n",
			"Wiki (disabled for students)",
		}, false},
		{"eclass_course_tools", map[string]any{"course": "NOPE"}, []string{"check the course code"}, true},
		{"eclass_get_page", map[string]any{"url": "modules/announcements/index.php?course=ICE251"}, []string{"| Ανακοίνωση |"}, false},
		{"eclass_submit_form", map[string]any{
			"url":    "modules/announcements/index.php?course=ICE251",
			"fields": []map[string]string{{"name": "draw", "value": "1"}},
			"ajax":   true,
		}, []string{`"recordsTotal": 1`, "[Νέο υλικό](" + srv.URL + "/modules/announcements/index.php?an_id=5)"}, false},
		{"eclass_get_page", map[string]any{"url": "https://evil.example.com/"}, []string{"refusing"}, true},
		{"eclass_get_page", map[string]any{"url": "modules/document/file.php/ICE251/slides.pdf"}, []string{"eclass_download"}, false},
		{"eclass_download", map[string]any{"url": "modules/document/file.php/ICE251/slides.pdf"}, []string{filepath.Join(dir, "Διάλεξη.pdf")}, false},
	}
	for _, tt := range tests {
		out, isErr := call(tt.name, tt.args)
		if isErr != tt.isError {
			t.Errorf("%s %v: isError=%v, want %v\n%s", tt.name, tt.args, isErr, tt.isError, out)
		}
		for _, w := range tt.want {
			if !strings.Contains(out, w) {
				t.Errorf("%s %v: missing %q in:\n%s", tt.name, tt.args, w, out)
			}
		}
	}

	if b, err := os.ReadFile(filepath.Join(dir, "Διάλεξη.pdf")); err != nil || string(b) != "%PDF-1.4 fake" {
		t.Errorf("downloaded file: %q, %v", b, err)
	}
}

func TestWindow(t *testing.T) {
	s := "αβγδε" // multi byte runes must never be split
	tests := []struct {
		w    pageWindow
		want string
	}{
		{pageWindow{}, "αβγδε"},
		{pageWindow{MaxChars: 2}, "αβ\n\n[showing characters 0 to 2 of 5, call again with offset=2 for more]"},
		{pageWindow{Offset: 3, MaxChars: 10}, "δε\n\n[showing characters 3 to 5 of 5]"},
		{pageWindow{Offset: 99}, "\n\n[showing characters 5 to 5 of 5]"},
		{pageWindow{Offset: -4, MaxChars: 1}, "α\n\n[showing characters 0 to 1 of 5, call again with offset=1 for more]"},
	}
	for _, tt := range tests {
		if got := window(s, tt.w); got != tt.want {
			t.Errorf("window(%+v) = %q, want %q", tt.w, got, tt.want)
		}
	}
}

func TestSessionExpired(t *testing.T) {
	u := func(s string) *url.URL { v, _ := url.Parse(s); return v }
	tests := []struct {
		url, body string
		want      bool
	}{
		{"https://e/modules/mobile/mcourses.php", " EXPIRED\n", true},
		{"https://e/main/login_form.php", "", true},
		{"https://e/", `<input name='uname'><input name="pass">`, true},
		{"https://e/modules/auth/courses.php", `<input type='password' name='pass123'><input name='uname_x'>`, false},
		{"https://e/main/portfolio.php", "<main>ok</main>", false},
	}
	for _, tt := range tests {
		if got := sessionExpired(u(tt.url), []byte(tt.body)); got != tt.want {
			t.Errorf("sessionExpired(%s, %q) = %v", tt.url, tt.body, got)
		}
	}
}

func TestHelpers(t *testing.T) {
	for ct, want := range map[string]bool{
		"": true, "text/html; charset=utf-8": true, "application/json": true,
		"application/xml": true, "text/calendar": true, "application/pdf": false, "image/png": false,
	} {
		if isText(ct) != want {
			t.Errorf("isText(%q) = %v", ct, !want)
		}
	}

	for in, want := range map[string]string{
		"https://e/modules/work/index.php?course=A%26view%3Dmobile": "https://e/modules/work/index.php?course=A",
		"https://e/main/notes/index.php?view=mobile":                "https://e/main/notes/index.php",
		"https://e/modules/usage/index.php?t=u&view=mobile":         "https://e/modules/usage/index.php?t=u",
	} {
		if got := cleanToolURL(in); got != want {
			t.Errorf("cleanToolURL(%q) = %q", in, got)
		}
	}

	for _, tt := range []struct{ disposition, path, want string }{
		{`attachment; filename="notes.pdf"`, "/x", "notes.pdf"},
		{`attachment; filename="../../.bashrc"`, "/x", ".bashrc"},
		{"", "/modules/document/file.php/C/lab%201.zip", "lab 1.zip"},
		{"", "/", "download"},
	} {
		u, _ := url.Parse("https://e" + tt.path)
		resp := &http.Response{Header: http.Header{"Content-Disposition": {tt.disposition}}, Request: &http.Request{URL: u}}
		if got := fileName(resp); got != tt.want {
			t.Errorf("fileName(%q, %q) = %q, want %q", tt.disposition, tt.path, got, tt.want)
		}
	}
}

func TestBuildRequestMultipart(t *testing.T) {
	file := filepath.Join(t.TempDir(), "report.pdf")
	if err := os.WriteFile(file, []byte("PDF"), 0o600); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse("https://e/modules/work/index.php?course=A")

	req, err := buildRequest(context.Background(), "POST", *u,
		[]Field{{"id", "7"}, {"recipients[]", "1"}, {"recipients[]", "2"}},
		[]Field{{"userfile[]", file}}, "tok")
	if err != nil {
		t.Fatal(err)
	}
	_, params, _ := mime.ParseMediaType(req.Header.Get("Content-Type"))
	form, err := multipart.NewReader(req.Body, params["boundary"]).ReadForm(1 << 20)
	if err != nil {
		t.Fatal(err)
	}
	if got := form.Value["recipients[]"]; len(got) != 2 || form.Value["id"][0] != "7" {
		t.Errorf("fields = %v", form.Value)
	}
	if _, ok := form.Value["token"]; ok {
		t.Error("token must only be added for mobile API endpoints")
	}
	if fh := form.File["userfile[]"]; len(fh) != 1 || fh[0].Filename != "report.pdf" {
		t.Errorf("files = %v", form.File)
	}

	if _, err := buildRequest(context.Background(), "POST", *u, nil, []Field{{"f", "/does/not/exist"}}, ""); err == nil {
		t.Error("missing upload file must fail")
	}

	m, _ := url.Parse("https://e/modules/mobile/mtools.php")
	req, _ = buildRequest(context.Background(), "GET", *m, []Field{{"course", "A"}}, nil, "tok")
	if q := req.URL.Query(); q.Get("token") != "tok" || q.Get("course") != "A" {
		t.Errorf("mobile query = %v", q)
	}
}
