package feed

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const sampleFeed = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd">
<channel>
<title>Sample Show</title>
<item>
<title>Ep 1</title>
<link>https://example.com/ep1</link>
<guid isPermaLink="false">guid-1</guid>
<itunes:episodeGuid>itunes-1</itunes:episodeGuid>
<pubDate>Mon, 29 Aug 2026 12:00:00 -0700</pubDate>
<enclosure url="https://example.com/ep1.mp3" type="audio/mpeg" length="123"/>
<enclosure url="https://example.com/ep1.mp4" type="video/mp4" length="456"/>
</item>
<item>
<title>Ep 2</title>
<link>https://example.com/ep2</link>
<guid>guid-2</guid>
<pubDate>Mon, 29 Aug 2026 13:00:00 +0000</pubDate>
<enclosure url="https://example.com/ep2.m4a" type="audio/mp4"/>
</item>
<item>
<title>Ep 3</title>
<link>https://example.com/ep3</link>
<pubDate>Tue, 30 Aug 2026 08:30:00 -0700</pubDate>
<enclosure url="https://example.com/ep3.mp3" type="audio/mpeg"/>
</item>
<item>
<title>Show notes, no audio</title>
<guid>guid-4</guid>
</item>
</channel>
</rss>`

func TestParse(t *testing.T) {
	show, err := Parse([]byte(sampleFeed))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if show.Title != "Sample Show" {
		t.Errorf("Title = %q, want %q", show.Title, "Sample Show")
	}
	if len(show.Episodes) != 3 {
		t.Fatalf("Episodes = %d, want 3 (item without enclosure skipped)", len(show.Episodes))
	}
	want := []Episode{
		{
			GUID:          "itunes-1",
			Title:         "Ep 1",
			EnclosureURL:  "https://example.com/ep1.mp3",
			EnclosureType: "audio/mpeg",
			PubDate:       "Mon, 29 Aug 2026 12:00:00 -0700",
		},
		{
			GUID:          "guid-2",
			Title:         "Ep 2",
			EnclosureURL:  "https://example.com/ep2.m4a",
			EnclosureType: "audio/mp4",
			PubDate:       "Mon, 29 Aug 2026 13:00:00 +0000",
		},
		{
			GUID:          "https://example.com/ep3",
			Title:         "Ep 3",
			EnclosureURL:  "https://example.com/ep3.mp3",
			EnclosureType: "audio/mpeg",
			PubDate:       "Tue, 30 Aug 2026 08:30:00 -0700",
		},
	}
	for i, w := range want {
		got := show.Episodes[i]
		if got != w {
			t.Errorf("episode %d = %+v, want %+v", i, got, w)
		}
	}
}

func TestParseGUIDChain(t *testing.T) {
	doc := `<?xml version="1.0"?>
<rss version="2.0" xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd">
<channel><title>Chain</title>
<item><title>A</title><link>link-a</link><guid>guid-a</guid><itunes:episodeGuid>itunes-a</itunes:episodeGuid>
<enclosure url="https://example.com/a.mp3" type="audio/mpeg"/></item>
<item><title>B</title><link>link-b</link><guid>guid-b</guid>
<enclosure url="https://example.com/b.mp3" type="audio/mpeg"/></item>
<item><title>C</title><link>link-c</link>
<enclosure url="https://example.com/c.mp3" type="audio/mpeg"/></item>
<item><title>D</title>
<enclosure url="https://example.com/d.mp3" type="audio/mpeg"/></item>
</channel></rss>`
	show, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(show.Episodes) != 4 {
		t.Fatalf("Episodes = %d, want 4", len(show.Episodes))
	}
	wantGUIDs := []string{"itunes-a", "guid-b", "link-c", ""}
	for i, w := range wantGUIDs {
		if got := show.Episodes[i].GUID; got != w {
			t.Errorf("episode %d GUID = %q, want %q", i, got, w)
		}
	}
}

func TestParseFirstEnclosureWins(t *testing.T) {
	doc := `<?xml version="1.0"?>
<rss version="2.0"><channel><title>M</title>
<item><title>X</title><guid>g</guid>
<enclosure url="https://example.com/x.mp3" type="audio/mpeg" length="1"/>
<enclosure url="https://example.com/x.mp4" type="video/mp4" length="2"/>
</item></channel></rss>`
	show, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := show.Episodes[0].EnclosureURL; got != "https://example.com/x.mp3" {
		t.Errorf("EnclosureURL = %q, want the first enclosure", got)
	}
	if got := show.Episodes[0].EnclosureType; got != "audio/mpeg" {
		t.Errorf("EnclosureType = %q, want audio/mpeg", got)
	}
}

func TestParseRejectsNonRSS(t *testing.T) {
	cases := map[string]string{
		"atom": `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom"><title>A</title></feed>`,
		"html": `<html><body>hi</body></html>`,
		"junk": `this is not xml`,
	}
	for name, body := range cases {
		if _, err := Parse([]byte(body)); err == nil {
			t.Errorf("%s: Parse succeeded, want error", name)
		}
	}
}

func TestFetchSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(sampleFeed))
	}))
	t.Cleanup(srv.Close)
	show, err := Fetch(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if show.Title != "Sample Show" || len(show.Episodes) != 3 {
		t.Errorf("Fetch = %+v, want Sample Show with 3 episodes", show)
	}
}

func TestFetchHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	_, err := Fetch(context.Background(), srv.Client(), srv.URL)
	if err == nil {
		t.Fatal("Fetch on 404 succeeded, want error")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error %q does not mention status 404", err)
	}
}

func TestFetchTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
	}))
	t.Cleanup(srv.Close)
	client := &http.Client{Timeout: 50 * time.Millisecond}
	if _, err := Fetch(context.Background(), client, srv.URL); err == nil {
		t.Fatal("Fetch with short timeout succeeded, want error")
	}
}

func TestFetchOversizedFeed(t *testing.T) {
	body := make([]byte, maxFeedBytes+16)
	for i := range body {
		body[i] = 'a'
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	_, err := Fetch(context.Background(), srv.Client(), srv.URL)
	if err == nil {
		t.Fatal("Fetch on an oversized feed succeeded, want error")
	}
	if !strings.Contains(err.Error(), "10 MiB") {
		t.Errorf("error %q does not name the 10 MiB cap", err)
	}
}

func TestParseMissingChannel(t *testing.T) {
	_, err := Parse([]byte(`<?xml version="1.0"?><rss version="2.0"></rss>`))
	if err == nil {
		t.Fatal("Parse of a channel-less feed succeeded, want error")
	}
}

func TestParseSkipsNonDownloadableEnclosures(t *testing.T) {
	doc := `<?xml version="1.0"?>
<rss version="2.0"><channel><title>Bad enclosures</title>
<item><title>Empty URL</title><guid>g1</guid>
<enclosure url="" type="audio/mpeg"/></item>
<item><title>Relative URL</title><guid>g2</guid>
<enclosure url="ep.mp3" type="audio/mpeg"/></item>
<item><title>Protocol-relative URL</title><guid>g3</guid>
<enclosure url="//example.com/ep.mp3" type="audio/mpeg"/></item>
<item><title>Non-http scheme</title><guid>g4</guid>
<enclosure url="ftp://example.com/ep.mp3" type="audio/mpeg"/></item>
<item><title>Good</title><guid>g5</guid>
<enclosure url="https://example.com/good.mp3" type="audio/mpeg"/></item>
</channel></rss>`
	show, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(show.Episodes) != 1 {
		t.Fatalf("Episodes = %d, want 1 (only the absolute http(s) enclosure): %+v", len(show.Episodes), show.Episodes)
	}
	if show.Episodes[0].GUID != "g5" {
		t.Errorf("GUID = %q, want g5", show.Episodes[0].GUID)
	}
}

func TestParseSelectsFirstDownloadableEnclosure(t *testing.T) {
	doc := `<?xml version="1.0"?>
<rss version="2.0"><channel><title>Enclosure order</title>
<item><title>Bad then good</title><guid>g1</guid>
<enclosure url="ep.mp3" type="audio/mpeg"/>
<enclosure url="https://example.com/real.m4a" type="audio/mp4"/></item>
<item><title>All bad</title><guid>g2</guid>
<enclosure url="" type="audio/mpeg"/>
<enclosure url="ftp://example.com/x.mp3" type="audio/mpeg"/></item>
</channel></rss>`
	show, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(show.Episodes) != 1 {
		t.Fatalf("Episodes = %d, want 1 (item with only invalid enclosures is skipped): %+v", len(show.Episodes), show.Episodes)
	}
	ep := show.Episodes[0]
	if ep.GUID != "g1" {
		t.Errorf("GUID = %q, want g1", ep.GUID)
	}
	if ep.EnclosureURL != "https://example.com/real.m4a" {
		t.Errorf("EnclosureURL = %q, want the valid second enclosure", ep.EnclosureURL)
	}
	if ep.EnclosureType != "audio/mp4" {
		t.Errorf("EnclosureType = %q, want audio/mp4", ep.EnclosureType)
	}
}

func TestFetchNilClientUsesDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(sampleFeed))
	}))
	t.Cleanup(srv.Close)
	show, err := Fetch(context.Background(), nil, srv.URL)
	if err != nil {
		t.Fatalf("Fetch with nil client: %v", err)
	}
	if show.Title != "Sample Show" || len(show.Episodes) != 3 {
		t.Errorf("Fetch = %+v, want Sample Show with 3 episodes", show)
	}
}
