package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestRenderPage(t *testing.T) {
	page, _ := url.Parse("https://eclass.example.gr/modules/work/index.php?course=CS101")
	html := `<html><head><title>Εργασίες</title></head><body>
	<header><a href="/logout">menu noise</a><form action="/search"><input type="hidden" name="token" value="sess-tok"></form></header>
	<main>
	  <script>$('#t').DataTable({'bServerSide': true, 'ajax': {'url': 'ajax_handler.php?mbox_type=inbox', type: 'POST'}});</script>
	  <h2>Εργασία 1</h2>
	  <a href="index.php?id=7">Λεπτομέρειες</a> <a href="?download=x"><span class="fa fa-download" data-bs-original-title="Λήψη"></span></a> <a href="javascript:void(0)">js</a>
	  <form method="post" action="submit.php" enctype="multipart/form-data">
	    <input type="hidden" name="token" value="abc123">
	    <label for="f1">Αρχείο</label><input id="f1" type="file" name="userfile">
	    <label><input type="radio" name="ans" value="3" checked> Παρίσι</label>
	    <select name="grp"><option value="1" selected>A</option><option value="2">B</option></select>
	    <textarea name="comments">hi</textarea>
	  </form>
	  <label aria-label="Εγγραφή"><input type="checkbox" name="selectCourse[]" value="8455"></label>
	</main></body></html>`

	out, err := renderPage(page, strings.NewReader(html))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"# Εργασίες",
		"https://eclass.example.gr/modules/work/index.php?id=7",
		"## Form 1: POST https://eclass.example.gr/modules/work/submit.php (accepts file uploads)",
		"`token` (hidden) = \"abc123\"",
		"`userfile` (file) = \"\" label: Αρχείο",
		"`ans` (radio) = \"3\" [checked] label: Παρίσι",
		"`grp` (select): \"1\"=A [selected], \"2\"=B",
		"`comments` (textarea) = \"hi\"",
		"POST https://eclass.example.gr/modules/work/ajax_handler.php?mbox_type=inbox",
		"## Fields outside any form",
		"`selectCourse[]` (checkbox) = \"8455\"",
		"CSRF token for this session: `sess-tok`",
		"[Λήψη](https://eclass.example.gr/modules/work/index.php?download=x)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"menu noise", "javascript:", "DataTable("} {
		if strings.Contains(out, unwanted) {
			t.Errorf("unexpected %q in:\n%s", unwanted, out)
		}
	}
}

// TestRelogin checks that an expired session triggers exactly one new login
// and the original request is retried with the new session.
func TestRelogin(t *testing.T) {
	logins := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/modules/mobile/mlogin.php":
			logins++
			if r.FormValue("uname") != "student" || r.FormValue("pass") != "secret" {
				w.Write([]byte("FAILED"))
				return
			}
			w.Write([]byte("sess" + string(rune('0'+logins))))
		case "/page":
			// Only the second session is valid, the first one "expired".
			if c, err := r.Cookie("PHPSESSID"); err != nil || c.Value != "sess2" {
				w.Write([]byte(`<form><input name='uname'><input name='pass'></form>`))
				return
			}
			w.Write([]byte("<main>hello</main>"))
		}
	}))
	defer srv.Close()

	c, _ := NewClient(srv.URL, "student", "secret")
	resp, err := c.Send(context.Background(), "GET", "page", nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := describe(resp)
	if !strings.Contains(out, "hello") || logins != 2 {
		t.Fatalf("logins=%d out=%q", logins, out)
	}

	if resp, err := c.Send(context.Background(), "GET", "https://evil.example.com/", nil, nil, false); err == nil {
		resp.Body.Close()
		t.Fatal("request to a foreign host must be refused")
	}

	bad, _ := NewClient(srv.URL, "student", "wrong")
	resp, err = bad.Send(context.Background(), "GET", "page", nil, nil, false)
	if err == nil {
		resp.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "wrong username") {
		t.Fatalf("want login error, got %v", err)
	}
}
