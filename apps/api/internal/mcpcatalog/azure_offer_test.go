//go:build !ee

package mcpcatalog

import "testing"

func TestAzureIsOfferedInCommunity(t *testing.T) {
	shipped, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := shipped.Get(azureConnectorID); !ok {
		t.Error("the Community catalog does not offer Azure")
	}
	if IsPreview(azureConnectorID) {
		t.Error("the Community build registers Azure for preview")
	}
}
