// Package feed fetches and parses RSS 2.0 podcast feeds.
package feed

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// maxFeedBytes caps feed downloads at 10 MiB; real feeds are far smaller.
const maxFeedBytes = 10 << 20

// FeedTimeout bounds feed fetches. Shared with the mirror package so the two
// cannot drift.
const FeedTimeout = 60 * time.Second

// UserAgent is sent with feed fetches and enclosure downloads.
const UserAgent = "podcastspeicher/1.0"

// Episode is one downloadable item of a podcast feed.
type Episode struct {
	GUID          string
	Title         string
	EnclosureURL  string
	EnclosureType string
	PubDate       string
}

// Show is a parsed RSS 2.0 podcast feed.
type Show struct {
	Title    string
	Episodes []Episode
}

// Fetch downloads the feed at rawURL and parses it as RSS 2.0. A non-2xx
// status, malformed XML, an oversized body, or a non-RSS document (e.g.
// Atom) is reported as an error so the caller can skip the show until the
// next poll.
func Fetch(ctx context.Context, client *http.Client, rawURL string) (Show, error) {
	if client == nil {
		client = &http.Client{Timeout: FeedTimeout}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return Show{}, fmt.Errorf("feed: %w", err)
	}
	req.Header.Set("User-Agent", UserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return Show{}, fmt.Errorf("feed: fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return Show{}, fmt.Errorf("feed: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFeedBytes+1))
	if err != nil {
		return Show{}, fmt.Errorf("feed: read: %w", err)
	}
	if int64(len(body)) > maxFeedBytes {
		return Show{}, fmt.Errorf("feed: body exceeds the %d MiB feed cap", maxFeedBytes>>20)
	}
	return Parse(body)
}

// Parse parses an RSS 2.0 feed document.
func Parse(data []byte) (Show, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	root, err := nextStartElement(dec)
	if err != nil {
		return Show{}, fmt.Errorf("feed: not well-formed XML: %w", err)
	}
	switch root.Name.Local {
	case "rss":
		// RSS 2.0, the only format supported in v1
	case "feed":
		return Show{}, fmt.Errorf("feed: Atom document; only RSS 2.0 is supported")
	default:
		return Show{}, fmt.Errorf("feed: unsupported root <%s>; only RSS 2.0 is supported", root.Name.Local)
	}
	var doc rssDoc
	if err := dec.DecodeElement(&doc, root); err != nil {
		return Show{}, fmt.Errorf("feed: parse: %w", err)
	}
	if doc.Channel == nil {
		return Show{}, fmt.Errorf("feed: no <channel> element; not a usable RSS feed")
	}
	show := Show{Title: strings.TrimSpace(doc.Channel.Title)}
	for _, it := range doc.Channel.Items {
		ep := Episode{
			Title:   strings.TrimSpace(it.Title),
			PubDate: strings.TrimSpace(it.PubDate),
		}
		// GUID chain: itunes:episodeGuid -> guid -> link. A feed without a
		// stable id yields an empty GUID; such episodes are deduped by file
		// existence alone.
		switch {
		case it.ItunesEpisodeGuid != "":
			ep.GUID = it.ItunesEpisodeGuid
		case it.GUID != "":
			ep.GUID = it.GUID
		default:
			ep.GUID = it.Link
		}
		// Select the first enclosure with a valid absolute http(s) URL; the
		// episode is skipped only when no enclosure is downloadable.
		for _, enc := range it.Enclosures {
			if isDownloadableURL(enc.URL) {
				ep.EnclosureURL = enc.URL
				ep.EnclosureType = enc.Type
				break
			}
		}
		if ep.EnclosureURL == "" {
			continue // nothing to mirror
		}
		show.Episodes = append(show.Episodes, ep)
	}
	return show, nil
}

// isDownloadableURL reports whether s is an absolute http(s) URL.
func isDownloadableURL(s string) bool {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return false
	}
	return u.Host != ""
}

func nextStartElement(dec *xml.Decoder) (*xml.StartElement, error) {
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		if se, ok := tok.(xml.StartElement); ok {
			return &se, nil
		}
		// skip prolog, comments, DOCTYPE, stray whitespace
	}
}

type rssDoc struct {
	Channel *channel `xml:"channel"`
}

type channel struct {
	Title string `xml:"title"`
	Items []item `xml:"item"`
}

type item struct {
	Title             string      `xml:"title"`
	Link              string      `xml:"link"`
	GUID              string      `xml:"guid"`
	PubDate           string      `xml:"pubDate"`
	ItunesEpisodeGuid string      `xml:"episodeGuid"`
	Enclosures        []enclosure `xml:"enclosure"`
}

type enclosure struct {
	URL    string `xml:"url,attr"`
	Type   string `xml:"type,attr"`
	Length string `xml:"length,attr"`
}
