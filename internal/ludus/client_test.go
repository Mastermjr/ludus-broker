package ludus

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestWireGuardAccessDoesNotSerializeOrFormatProfile(t *testing.T) {
	const secret = "private-key-material"
	access := NewWireGuardAccess(WireGuardPeer{UserID: "user1", Generation: 1, PeerID: "sha256:peer"}, []byte("[Interface]\nPrivateKey = "+secret))
	defer access.Destroy()
	encoded, err := json.Marshal(access)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) || strings.Contains(fmt.Sprintf("%v", access), secret) || strings.Contains(fmt.Sprintf("%+v", access), secret) {
		t.Fatal("WireGuard private profile leaked through serialization or formatting")
	}
	profile, err := access.ConsumeProfile()
	if err != nil || !strings.Contains(string(profile), secret) {
		t.Fatalf("profile unavailable: %v", err)
	}
	zeroBytes(profile)
	if _, err := access.ConsumeProfile(); err == nil {
		t.Fatal("profile could be consumed twice")
	}
}
