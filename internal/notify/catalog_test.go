package notify

import "testing"

func TestCatalogCompleteAndReadOnly(t *testing.T) {
	wantTypes := []string{
		KeySessionOffline,
		KeyBotSendFailures,
		KeyTaskFailures,
		KeyStoreWriteFailed,
		KeyTempDirUsage,
		KeyStartupRecovered,
		KeyMediaConfigInvalid,
		KeyBotListInvalid,
		KeyBotInitFailed,
		KeyBotPollConflict,
		KeyCloudUploadFailed,
		KeyCloudConfigInvalid,
		KeyCloudDisabled,
		KeyWebAdminLogin,
		KeyUserApplication,
		KeyChannelJoinRequest,
	}
	validCategories := map[string]bool{
		CategorySystemAlert: true, CategorySystemRecovery: true, CategoryActivity: true,
	}
	validSeverities := map[string]bool{SeverityInfo: true, SeverityWarn: true, SeverityError: true}

	catalog := Catalog()
	if len(catalog) != len(wantTypes) {
		t.Fatalf("catalog size=%d, want %d", len(catalog), len(wantTypes))
	}
	seen := make(map[string]bool, len(catalog))
	for _, definition := range catalog {
		if definition.Type == "" || definition.TypeLabel == "" || definition.Title == "" || definition.Description == "" {
			t.Fatalf("incomplete definition: %+v", definition)
		}
		if !validCategories[definition.Category] {
			t.Fatalf("invalid category in %+v", definition)
		}
		if !validSeverities[definition.Severity] {
			t.Fatalf("invalid severity in %+v", definition)
		}
		if seen[definition.Type] {
			t.Fatalf("duplicate event type %q", definition.Type)
		}
		seen[definition.Type] = true
		got, ok := GetDefinition(definition.Type)
		if !ok || got != definition {
			t.Fatalf("GetDefinition(%q)=%+v,%v", definition.Type, got, ok)
		}
	}
	for _, eventType := range wantTypes {
		if !seen[eventType] {
			t.Errorf("missing event type %q", eventType)
		}
	}

	catalog[0].Title = "mutated"
	fresh := Catalog()
	if fresh[0].Title == "mutated" {
		t.Fatal("Catalog returned mutable backing storage")
	}
}
