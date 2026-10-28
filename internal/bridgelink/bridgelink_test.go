package bridgelink

import "testing"

func TestKeysRoundTrip(t *testing.T) {
	keys, err := GenerateKeys()
	if err != nil {
		t.Fatalf("GenerateKeys: %v", err)
	}
	pub, err := PublicFromPrivate(keys.PrivateKey)
	if err != nil {
		t.Fatalf("PublicFromPrivate: %v", err)
	}
	if pub != keys.PublicKey {
		t.Fatalf("the public key derived from the private one does not match:\n  got  %s\n  want %s", pub, keys.PublicKey)
	}
	if len(keys.ShortID) != 16 { // 8 bytes → 16 hex characters
		t.Fatalf("short_id length %d, expected 16", len(keys.ShortID))
	}
}

func TestDescriptorRoundTrip(t *testing.T) {
	in := Descriptor{
		Host:      "203.0.113.7",
		Port:      443,
		PublicKey: "JGiyWleKke7suoXKMwujFww0OzyudctAlPjvr2z0bRc",
		ShortID:   "08b992eec8cf3b84",
		SNI:       "dl.google.com",
		Msg:       true,
		Calls:     true,
		Geo:       "RU",
	}
	link, err := Encode(in)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	out, err := Decode(link)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if out != in {
		t.Fatalf("round trip mismatch:\n  got  %+v\n  want %+v", out, in)
	}
}

func TestDecodeRejectsGarbage(t *testing.T) {
	if _, err := Decode("https://example.com/not-a-bridge"); err == nil {
		t.Fatal("expected an error for a link without the scheme prefix")
	}
}
