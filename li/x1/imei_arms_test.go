// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"strings"
	"testing"

	"github.com/omec-project/util/li/store"
	"github.com/omec-project/util/li/types"
)

// imeiWithCheckDigit is a widely published example IMEI: fourteen digits and their check
// digit, 8.
const (
	imeiWithCheckDigit = "490154203237518"
	imeiFourteen       = "49015420323751"
)

// TestEveryIMEIArmNamesTheSameDevice: an ADMF can name a device by the fourteen-digit IMEI
// in either of two elements, or with its check digit in a third. All three must be matched
// as the fourteen-digit PEI the network functions present -- before, imei and
// peiImeiCheckDigit were refused as unsupported, so the usual operator input, the IMEI as
// printed on the device, could not be tasked at all.
func TestEveryIMEIArmNamesTheSameDevice(t *testing.T) {
	for _, element := range []string{"peiImei", "imei", "peiImeiCheckDigit"} {
		t.Run(element, func(t *testing.T) {
			value := imeiFourteen
			if element == "peiImeiCheckDigit" {
				value = imeiWithCheckDigit
			}

			st := store.New()
			mustAck(t, testServer(st), withTarget("<ns1:"+element+">"+value+"</ns1:"+element+">"))

			task, ok := st.Get(testXID)
			if !ok {
				t.Fatal("task not stored")
			}
			want := types.TargetIdentifier{Type: types.TargetPEI, Value: imeiFourteen}
			if len(task.Targets) != 1 || task.Targets[0] != want {
				t.Errorf("stored %+v, want %+v", task.Targets, want)
			}
		})
	}
}

// TestACheckDigitThatDoesNotMatchIsRefused: the check digit is dropped when the IMEI is
// stored, so it is verified rather than discarded unread. A wrong one means the value
// names no device; acknowledging it would intercept whatever device the first fourteen
// digits happen to name, on a warrant that named something else.
//
// A wrong digit is schema-valid -- the type is fifteen digits -- so it is the operation's own
// refusal and says what is wrong; a wrong length is outside the type and stays 1010.
func TestACheckDigitThatDoesNotMatchIsRefused(t *testing.T) {
	for _, c := range []struct {
		value, mention string
		code           int
	}{
		{"490154203237517", "check digit does not match", errCodeActivateFailed},
		{"49015420323751", "does not conform to its schema type", errCodeSchemaError},
		{"4901542032375180", "does not conform to its schema type", errCodeSchemaError},
	} {
		st := store.New()
		m := serve(t, testServer(st), withTarget("<ns1:peiImeiCheckDigit>"+c.value+"</ns1:peiImeiCheckDigit>"))

		if m.ErrorInformation == nil || m.ErrorInformation.ErrorCode != c.code {
			t.Errorf("%s: want a %d refusal, got %+v", c.value, c.code, m.ErrorInformation)
		} else if !strings.Contains(m.ErrorInformation.ErrorDescription, c.mention) {
			t.Errorf("%s: the refusal says %q, want it to say %q", c.value,
				m.ErrorInformation.ErrorDescription, c.mention)
		}
		if _, held := st.Get(testXID); held {
			t.Errorf("%s: a refused task was stored", c.value)
		}
	}
}

// TestACheckDigitIMEIIsAnsweredAsSent: the task is stored without the check digit, and the
// answer uses the element the request used, whose type is fifteen digits. Rendering the
// stored value alone would make the details answer invalid; the check digit is re-derived
// instead, which reproduces what the ADMF sent because a wrong one was refused.
func TestACheckDigitIMEIIsAnsweredAsSent(t *testing.T) {
	requireXmllint(t)

	out := detailsAnswer(t, testServer(store.New()),
		"<ns1:peiImeiCheckDigit>"+imeiWithCheckDigit+"</ns1:peiImeiCheckDigit>")

	if want := "<ns1:peiImeiCheckDigit>" + imeiWithCheckDigit + "</ns1:peiImeiCheckDigit>"; !strings.Contains(out, want) {
		t.Errorf("the answer does not carry the IMEI as sent:\n%s", out)
	}
	if problems := validateAgainstSchema(t, []byte(out)); len(problems) > 0 {
		t.Errorf("the answer is invalid against the schema: %v\n%s", problems, out)
	}
}

func TestIMEICheckDigit(t *testing.T) {
	for imei, want := range map[string]byte{
		imeiFourteen:     '8',
		"35209900176148": '1', // a second published example
		"00000000000000": '0',
	} {
		if got := imeiCheckDigit(imei); got != want {
			t.Errorf("imeiCheckDigit(%s) = %c, want %c", imei, got, want)
		}
	}
}
