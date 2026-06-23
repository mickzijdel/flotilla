package daemon

import (
	"testing"
	"time"
)

func TestAgentRecordRoundTrip(t *testing.T) {
	p := Paths{Root: t.TempDir()}
	if r, err := p.LoadAgent("ghost"); err != nil || r.Name != "" {
		t.Fatalf("missing record should be zero: %+v %v", r, err)
	}
	rec := AgentRecord{Name: "otter", LastStatus: "done", LastHandledSHA: "abc", LastSubmittedSHA: "abc", LastEventTS: time.Unix(100, 0).UTC()}
	if err := p.SaveAgent(rec); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := p.LoadAgent("otter")
	if err != nil || got.LastHandledSHA != "abc" || got.LastStatus != "done" {
		t.Fatalf("round-trip mismatch: %+v %v", got, err)
	}
	all, err := p.ListAgentRecords()
	if err != nil || len(all) != 1 || all[0].Name != "otter" {
		t.Fatalf("list: %+v %v", all, err)
	}
}

func TestVersionStamp(t *testing.T) {
	p := Paths{Root: t.TempDir()}
	if v := p.ReadVersion(); v != "" {
		t.Fatalf("missing version should be empty, got %q", v)
	}
	if err := p.WriteVersion("123-456"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if v := p.ReadVersion(); v != "123-456" {
		t.Fatalf("got %q", v)
	}
}
