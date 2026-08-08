package merchant

import "testing"

func TestSignMatchesTheAgreedRule(t *testing.T) {
	// sha256("a=1&b=2sfejo")
	const want = "5e0b85b99f35738c26c0299d1d79cbd20a3b7df7a00ab8d81d5e831d1d300f54"
	params, err := DecodeParams([]byte(`{"b":2,"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := Sign(params, "sfejo"); got != want {
		t.Fatalf("Sign = %s, want %s", got, want)
	}
	// Numbers must format identically whichever decoder produced them.
	if got := Sign(map[string]any{"a": 1, "b": 2}, "sfejo"); got != want {
		t.Fatalf("Sign on native ints = %s, want %s", got, want)
	}
}

func TestSignIgnoresSignFieldAndEmptyValues(t *testing.T) {
	base := Sign(map[string]any{"a": "1"}, "s")
	withSign := Sign(map[string]any{"a": "1", "sign": "whatever"}, "s")
	withEmpty := Sign(map[string]any{"a": "1", "b": "", "c": nil}, "s")
	if base != withSign || base != withEmpty {
		t.Fatalf("sign, empty and nil values must be excluded: %s %s %s", base, withSign, withEmpty)
	}
}

func TestSignIsOrderIndependentButSecretSensitive(t *testing.T) {
	one := Sign(map[string]any{"merchant_id": "m1", "uid": "1001"}, "s")
	two := Sign(map[string]any{"uid": "1001", "merchant_id": "m1"}, "s")
	if one != two {
		t.Fatal("the signature must not depend on map iteration order")
	}
	if one == Sign(map[string]any{"merchant_id": "m1", "uid": "1001"}, "other") {
		t.Fatal("a different secret must produce a different signature")
	}
}

func TestVerify(t *testing.T) {
	params := map[string]any{"merchant_id": "m1", "uid": "1001"}
	params[SignField] = Sign(params, "s")
	if err := Verify(params, "s"); err != nil {
		t.Fatalf("a correctly signed request must pass: %v", err)
	}
	if err := Verify(params, "wrong"); err == nil {
		t.Fatal("a request signed with another secret must be rejected")
	}
	// Tampering with a signed parameter must invalidate the signature.
	params["uid"] = "1002"
	if err := Verify(params, "s"); err == nil {
		t.Fatal("a tampered request must be rejected")
	}
	delete(params, SignField)
	if err := Verify(params, "s"); err == nil {
		t.Fatal("an unsigned request must be rejected")
	}
}

func TestSignedPayloadIsVerifiable(t *testing.T) {
	blob, err := SignedPayload(map[string]any{"amount": "1000000", "uid": "1001"}, "s")
	if err != nil {
		t.Fatal(err)
	}
	params, err := DecodeParams(blob)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(params, "s"); err != nil {
		t.Fatalf("a merchant callback must verify with the merchant secret: %v", err)
	}
}

func TestAccountIsUniquePerMerchant(t *testing.T) {
	if Account("m1", "1001") == Account("m2", "1001") {
		t.Fatal("the same uid under two merchants must be two accounts")
	}
}
