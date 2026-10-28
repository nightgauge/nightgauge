package blogwatch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
)

func TestCheckAllowed(t *testing.T) {
	hosts := []string{"www.anthropic.com"}
	for raw, ok := range map[string]bool{
		"https://www.anthropic.com/sitemap.xml": true,
		"https://WWW.Anthropic.com/x":           true,
		"http://www.anthropic.com/sitemap.xml":  false,
		"https://anthropic.com/sitemap.xml":     false,
		"https://evil.example/x":                false,
		"/relative":                             false,
	} {
		if got := CheckAllowed(raw, hosts) == nil; got != ok {
			t.Errorf("CheckAllowed(%q) = %v, want %v", raw, got, ok)
		}
	}
}

func TestExtractPostURLs(t *testing.T) {
	doc := `<urlset><url><loc>https://blog.test/engineering/a</loc></url>
<url><loc>https://blog.test/engineering/a/</loc></url>
<url><loc>https://blog.test/news/x</loc></url>
<url><loc>http://blog.test/engineering/insecure</loc></url>
<url><loc>https://other.test/engineering/c</loc></url></urlset>
<a href="https://blog.test/engineering/b?utm=1#top">b</a>
<entry><link href="https://blog.test/engineering/c"/></entry>
<a href="https://blog.test/engineering">index itself</a>`
	got := ExtractPostURLs(doc, "https://blog.test/sitemap.xml", "https://blog.test/engineering/")
	want := []string{"https://blog.test/engineering/a", "https://blog.test/engineering/b", "https://blog.test/engineering/c"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// fixture serves an index on a TLS server and counts requests.
func fixture(t *testing.T, index *string) (*httptest.Server, *int64) {
	t.Helper()
	var hits int64
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		_, _ = w.Write([]byte(*index))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestDetect(t *testing.T) {
	index := ""
	srv, hits := fixture(t, &index)
	host := mustHost(t, srv.URL)
	index = `<loc>` + srv.URL + `/eng/one</loc><loc>` + srv.URL + `/eng/two</loc>`
	f := &Fetcher{Client: srv.Client(), AllowedHosts: []string{host}}
	sources := []Source{{Name: "fx", IndexURL: srv.URL + "/sitemap.xml", PostURLPrefix: srv.URL + "/eng/"}}

	res := Detect(context.Background(), f, sources, []Decision{{URL: srv.URL + "/eng/one"}})
	if len(res.New) != 1 || res.New[0].URL != srv.URL+"/eng/two" || res.KnownCount != 1 {
		t.Fatalf("new = %+v", res)
	}
	res = Detect(context.Background(), f, sources, []Decision{{URL: srv.URL + "/eng/one"}, {URL: srv.URL + "/eng/two"}})
	if len(res.New) != 0 {
		t.Fatalf("unchanged index gave new = %+v", res.New)
	}
	if *hits != 2 {
		t.Fatalf("hits = %d, want 2", *hits)
	}
}

func TestDetectNeverFetchesOffAllowlist(t *testing.T) {
	index := "<loc>x</loc>"
	srv, hits := fixture(t, &index)
	f := &Fetcher{Client: srv.Client(), AllowedHosts: []string{"www.anthropic.com"}}
	plain := "http" + srv.URL[len("https"):]
	res := Detect(context.Background(), f, []Source{
		{Name: "offlist", IndexURL: srv.URL + "/s", PostURLPrefix: srv.URL + "/"},
		{Name: "plain", IndexURL: plain + "/s", PostURLPrefix: plain + "/"},
	}, nil)
	if len(res.Skipped) != 2 || *hits != 0 {
		t.Fatalf("skipped = %+v, hits = %d", res.Skipped, *hits)
	}
}

func TestDetectReportsEmptyIndex(t *testing.T) {
	index := "<html>nothing</html>"
	srv, _ := fixture(t, &index)
	f := &Fetcher{Client: srv.Client(), AllowedHosts: []string{mustHost(t, srv.URL)}}
	res := Detect(context.Background(), f, []Source{{Name: "fx", IndexURL: srv.URL + "/s", PostURLPrefix: srv.URL + "/eng/"}}, nil)
	if len(res.Skipped) != 1 || len(res.New) != 0 {
		t.Fatalf("res = %+v", res)
	}
}

func TestHTMLToText(t *testing.T) {
	got := HTMLToText(`<html><script>evil()</script><style>x{}</style><p>Hello &amp; <b>world</b></p></html>`)
	if got != "Hello &\nworld" {
		t.Fatalf("got %q", got)
	}
}

func mustHost(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Hostname()
}

func TestFetchRefusesRedirectOffAllowlistBeforeSending(t *testing.T) {
	var offHits int64
	off := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&offHits, 1)
	}))
	t.Cleanup(off.Close)
	on := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, off.URL+"/internal", http.StatusFound)
	}))
	t.Cleanup(on.Close)
	f := &Fetcher{Client: on.Client(), AllowedHosts: []string{mustHost(t, on.URL)}}
	if _, err := f.Fetch(context.Background(), on.URL+"/s"); err == nil {
		t.Fatal("redirect off the allowlist was followed")
	}
	if offHits != 0 {
		t.Fatalf("off-allowlist target received %d request(s)", offHits)
	}
	if on.Client().CheckRedirect != nil {
		t.Fatal("caller's client was mutated")
	}
}
