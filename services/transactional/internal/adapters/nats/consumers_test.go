package nats

import "testing"

// sending_ready solo cuenta si viene como booleano: ausente, nulo o de otro tipo es no saberlo.
func TestSendingReadySoloSiVieneComoBooleano(t *testing.T) {
	if got := optionalBool(true); got == nil || !*got {
		t.Error("true")
	}
	if got := optionalBool(false); got == nil || *got {
		t.Error("false")
	}
	for _, v := range []interface{}{nil, "true", 1.0} {
		if optionalBool(v) != nil {
			t.Errorf("%#v no es un booleano", v)
		}
	}
}
