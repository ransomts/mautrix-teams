package model

import (
	"encoding/json"
	"testing"
)

func TestParseMentionsFromHTMLBasic(t *testing.T) {
	html := `Hello <span itemtype="http://schema.skype.com/Mention" itemid="0">Alice</span>, meet <span itemtype="http://schema.skype.com/Mention" itemid="1">Bob</span>.`
	mentions := ParseMentionsFromHTML(html)
	if len(mentions) != 2 {
		t.Fatalf("expected 2 mentions, got %d", len(mentions))
	}
	if mentions[0].DisplayName != "Alice" || mentions[0].ItemID != "0" {
		t.Fatalf("unexpected first mention: %#v", mentions[0])
	}
	if mentions[1].DisplayName != "Bob" || mentions[1].ItemID != "1" {
		t.Fatalf("unexpected second mention: %#v", mentions[1])
	}
}

func TestParseMentionsFromHTMLNoMentions(t *testing.T) {
	if mentions := ParseMentionsFromHTML("plain text"); mentions != nil {
		t.Fatalf("expected nil, got %#v", mentions)
	}
	if mentions := ParseMentionsFromHTML("<p>no mention spans</p>"); mentions != nil {
		t.Fatalf("expected nil for HTML without mentions, got %#v", mentions)
	}
}

func TestParseMentionsFromHTMLEmptyDisplayName(t *testing.T) {
	html := `<span itemtype="http://schema.skype.com/Mention" itemid="0"></span>`
	mentions := ParseMentionsFromHTML(html)
	if len(mentions) != 0 {
		t.Fatalf("expected no mentions for empty display name, got %d", len(mentions))
	}
}

func TestExtractMentionMRIsNumericID(t *testing.T) {
	props := json.RawMessage(`{"mentions":[{"id":0,"mri":"8:orgid:abc-123","displayName":"Alice"},{"id":1,"mri":"8:orgid:def-456","displayName":"Bob"}]}`)
	result := ExtractMentionMRIs(props)
	if len(result) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(result))
	}
	if result["0"] != "8:orgid:abc-123" {
		t.Fatalf("unexpected MRI for id 0: %q", result["0"])
	}
	if result["1"] != "8:orgid:def-456" {
		t.Fatalf("unexpected MRI for id 1: %q", result["1"])
	}
}

func TestExtractMentionMRIsStringID(t *testing.T) {
	props := json.RawMessage(`{"mentions":[{"id":"0","mri":"8:orgid:abc-123","displayName":"Alice"}]}`)
	result := ExtractMentionMRIs(props)
	if len(result) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(result))
	}
	if result["0"] != "8:orgid:abc-123" {
		t.Fatalf("unexpected MRI: %q", result["0"])
	}
}

func TestExtractMentionMRIsEmptyInput(t *testing.T) {
	if result := ExtractMentionMRIs(nil); result != nil {
		t.Fatalf("expected nil for nil input, got %#v", result)
	}
	if result := ExtractMentionMRIs(json.RawMessage(`{}`)); result != nil {
		t.Fatalf("expected nil for empty object, got %#v", result)
	}
	if result := ExtractMentionMRIs(json.RawMessage(`{"mentions":[]}`)); result != nil {
		t.Fatalf("expected nil for empty mentions, got %#v", result)
	}
}

func TestExtractMentionMRIsSkipsEmptyMRI(t *testing.T) {
	props := json.RawMessage(`{"mentions":[{"id":0,"mri":"","displayName":"Ghost"}]}`)
	result := ExtractMentionMRIs(props)
	if len(result) != 0 {
		t.Fatalf("expected empty map, got %#v", result)
	}
}

func TestResolveMentions(t *testing.T) {
	htmlMentions := []TeamsMention{
		{DisplayName: "Alice", ItemID: "0"},
		{DisplayName: "Bob", ItemID: "1"},
		{DisplayName: "Unknown", ItemID: "99"},
	}
	mriMap := map[string]string{
		"0": "8:orgid:alice-uuid",
		"1": "8:orgid:bob-uuid",
	}
	resolved := ResolveMentions(htmlMentions, mriMap)
	if len(resolved) != 3 {
		t.Fatalf("expected 3 resolved mentions, got %d", len(resolved))
	}
	if resolved[0].UserID != "8:orgid:alice-uuid" {
		t.Fatalf("expected Alice MRI, got %q", resolved[0].UserID)
	}
	if resolved[1].UserID != "8:orgid:bob-uuid" {
		t.Fatalf("expected Bob MRI, got %q", resolved[1].UserID)
	}
	if resolved[2].UserID != "" {
		t.Fatalf("expected empty MRI for unknown, got %q", resolved[2].UserID)
	}
}

func TestResolveMentionsNilMap(t *testing.T) {
	htmlMentions := []TeamsMention{{DisplayName: "Alice", ItemID: "0"}}
	resolved := ResolveMentions(htmlMentions, nil)
	if len(resolved) != 1 {
		t.Fatalf("expected 1 mention, got %d", len(resolved))
	}
	if resolved[0].UserID != "" {
		t.Fatalf("expected empty user id with nil map, got %q", resolved[0].UserID)
	}
}

func TestResolveMentionsEmpty(t *testing.T) {
	if resolved := ResolveMentions(nil, nil); resolved != nil {
		t.Fatalf("expected nil, got %#v", resolved)
	}
}
