package controller

import (
	"reflect"
	"testing"
)

func TestOutboundTagsFromTemplateDeduplicatesAndSkipsEmptyTags(t *testing.T) {
	raw := `{"outbounds":[{"tag":"proxy-a"},{"tag":""},{"tag":"proxy-b"},{"tag":"proxy-a"}]}`
	got, err := outboundTagsFromTemplate(raw)
	if err != nil {
		t.Fatalf("outboundTagsFromTemplate() error = %v", err)
	}
	want := []string{"proxy-a", "proxy-b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("outboundTagsFromTemplate() = %#v, want %#v", got, want)
	}
}

func TestOutboundTagsFromTemplateRejectsInvalidJSON(t *testing.T) {
	if _, err := outboundTagsFromTemplate(`{"outbounds":`); err == nil {
		t.Fatal("outboundTagsFromTemplate() expected an error for invalid JSON")
	}
}

func TestAutoInboundTagIsStableAndScopedToOutbound(t *testing.T) {
	first := autoInboundTag("proxy-a")
	if first != autoInboundTag("proxy-a") {
		t.Fatal("autoInboundTag() should be deterministic")
	}
	if first == autoInboundTag("proxy-b") {
		t.Fatal("different outbound tags must not share an inbound tag")
	}
	if len(first) != len("auto-in-")+16 {
		t.Fatalf("unexpected generated tag length %d: %q", len(first), first)
	}
}
