// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package iri

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// TestARecordViolatingItsOwnDefinitionIsRefused is the encoder validating what it emits.
//
// The record definitions carry their restrictions in their type comments — `OCTET STRING
// (SIZE(16..65540))`, `INTEGER (0..255)` — and EncodeXIRI enforces them. A record violating its
// own definition would otherwise be invisible on both sides: this element would believe it
// delivered, a conformant mediation function would discard what it cannot validate, and because
// the delivery succeeded no fault would be raised anywhere.
//
// The cases cover each kind of restriction — a size, a range, an enumeration, a length-bounded
// string and a NumericString alphabet — reached through the fields that carry them.
// TestEveryConstraintBoundIsEnforced checks every table entry at its bounds.
func TestARecordViolatingItsOwnDefinitionIsRefused(t *testing.T) {
	conformantPolicy := make(UEPolicy, 16)

	for _, tc := range []struct {
		name  string
		event any
		// want is a fragment of the refusal, so the message says which field and which
		// definition rather than only that something was wrong.
		want string
	}{
		{
			name:  "an octet string shorter than its SIZE",
			event: AMFUEPolicyTransfer{SUPI: IMSI("262019876543210"), UEPolicy: make(UEPolicy, 15)},
			want:  "UEPolicy is defined as SIZE(16..65540)",
		},
		{
			name: "an address whose length is not its family's",
			event: SMFPDUSessionEstablishment{
				SUPI:           IMSI("262019876543210"),
				PDUSessionType: PDUSessionTypeIPv4,
				UEEndpoint:     []any{IPv4Address{10, 250, 0}},
			},
			want: "IPv4Address is defined as SIZE(4)",
		},
		{
			name: "an integer above its range",
			event: AMFRANHandoverRequest{
				UserIdentifiers: sampleIdentifiers(),
				// AMFUENGAPID ::= INTEGER (0..1099511627775).
				AMFUENGAPID: 1099511627776,
				RANUENGAPID: 2,
			},
			want: "AMFUENGAPID is defined as INTEGER (0..1099511627775)",
		},
		{
			name: "a UTF8String longer than its SIZE",
			event: AMFPositioningInfoTransfer{
				SUPI:             IMSI("262019876543210"),
				LCSCorrelationID: LCSCorrelationID(strings.Repeat("x", 256)),
			},
			want: "LCSCorrelationID is defined as UTF8String (SIZE(1..255))",
		},
		{
			// An ENUMERATED, which is the restriction reachable from peer input: an AMF builds
			// this record from the handover type in the gNB's NGAP message.
			name: "a value outside an enumeration",
			event: AMFRANHandoverRequest{
				UserIdentifiers: sampleIdentifiers(),
				AMFUENGAPID:     1,
				RANUENGAPID:     2,
				HandoverType:    HandoverType(9),
			},
			want: "HandoverType is an ENUMERATED with values 1..4",
		},
		{
			// The HandoverCause arms are bounded at both ends, so a negative cause is refused
			// against the range.
			name: "a cause below the first value of every group",
			event: AMFRANHandoverRequest{
				UserIdentifiers: sampleIdentifiers(),
				AMFUENGAPID:     1,
				RANUENGAPID:     2,
				HandoverType:    HandoverIntra5GS,
				HandoverCause:   CauseRadioNetwork(-1),
			},
			want: "CauseRadioNetwork is an ENUMERATED with values 1..52",
		},
		{
			// The upper bound, which is the direction a value from another protocol arrives
			// from: NGAP's radio-network group runs to 57 and TS 33.128's to 52, so a value
			// mapped carelessly lands here.
			name: "a cause above its group's last value",
			event: AMFRANHandoverRequest{
				UserIdentifiers: sampleIdentifiers(),
				AMFUENGAPID:     1,
				RANUENGAPID:     2,
				HandoverType:    HandoverIntra5GS,
				HandoverCause:   CauseRadioNetwork(53),
			},
			want: "CauseRadioNetwork is an ENUMERATED with values 1..52",
		},
		{
			// And a group whose bound differs, so the table is being consulted per type rather
			// than one bound standing for all five. NGAP's CauseNas runs to 5; TS 33.128's to 4.
			name: "a NAS cause NGAP defines and TS 33.128 does not",
			event: AMFRANHandoverRequest{
				UserIdentifiers: sampleIdentifiers(),
				AMFUENGAPID:     1,
				RANUENGAPID:     2,
				HandoverType:    HandoverIntra5GS,
				HandoverCause:   CauseNas(5),
			},
			want: "CauseNas is an ENUMERATED with values 1..4",
		},

		// The named leaves, each reached through a field that carries it.
		{
			// NGAP numbers handoverType from zero and TS 33.128 from one, so an intra-5GS
			// handover cast across unmapped arrives as 0 — a value the enumeration does not
			// define, in a member the record makes mandatory. A conformant decoder refuses the
			// whole record, so zero is refused here rather than treated as absence.
			name: "a mandatory enumerated member carrying its source protocol's first value",
			event: AMFRANHandoverRequest{
				UserIdentifiers: sampleIdentifiers(),
				AMFUENGAPID:     1,
				RANUENGAPID:     2,
				HandoverType:    HandoverType(0),
				HandoverCause:   CauseRadioNetwork(1),
			},
			want: "HandoverType is an ENUMERATED with values 1..4",
		},
		{
			// And the same rule for the cause arms, which are mandatory wherever they appear.
			// The mapping cannot produce zero, so a zero is the mapping having been bypassed.
			name: "a cause arm carrying zero, which the mapping cannot produce",
			event: AMFRANHandoverRequest{
				UserIdentifiers: sampleIdentifiers(),
				AMFUENGAPID:     1,
				RANUENGAPID:     2,
				HandoverType:    HandoverIntra5GS,
				HandoverCause:   CauseNas(0),
			},
			want: "CauseNas is an ENUMERATED with values 1..4",
		},
		{
			name: "a slice differentiator that is not three octets",
			event: SMFPDUSessionEstablishment{
				SUPI:           IMSI("262019876543210"),
				PDUSessionID:   5,
				GTPTunnelID:    FTEID{TEID: 1, IPv4Address: IPv4Address{10, 250, 0, 1}},
				PDUSessionType: PDUSessionTypeIPv4,
				SNSSAI:         SNSSAI{SliceServiceType: 1, SliceDifferentiator: SliceDifferentiator{0x01, 0x02}},
			},
			want: "SliceDifferentiator is defined as SIZE(3)",
		},
		{
			// The same leaf reached through the *other* field that carries it. This is what
			// keying on the type buys and keying on a field name would not: the mapped HPLMN
			// differentiator is spelled differently and inherits the check anyway.
			name: "a mapped HPLMN slice differentiator of the wrong size",
			event: SMFPDUSessionEstablishment{
				SUPI:           IMSI("262019876543210"),
				PDUSessionID:   5,
				GTPTunnelID:    FTEID{TEID: 1, IPv4Address: IPv4Address{10, 250, 0, 1}},
				PDUSessionType: PDUSessionTypeIPv4,
				SNSSAI: SNSSAI{
					SliceServiceType: 1,
					MappedHPLMNSD:    SliceDifferentiator{0x01, 0x02, 0x03, 0x04},
				},
			},
			want: "SliceDifferentiator is defined as SIZE(3)",
		},
		{
			name: "a slice service type above its range",
			event: SMFPDUSessionEstablishment{
				SUPI:           IMSI("262019876543210"),
				PDUSessionID:   5,
				GTPTunnelID:    FTEID{TEID: 1, IPv4Address: IPv4Address{10, 250, 0, 1}},
				PDUSessionType: PDUSessionTypeIPv4,
				SNSSAI:         SNSSAI{SliceServiceType: 256},
			},
			want: "SliceServiceType is defined as INTEGER (0..255)",
		},
		{
			// A TEID is 32 bits on the wire and an int64 in Go, so the range is the only thing
			// between a 33-bit value and a record a receiver cannot read.
			name: "a TEID above 32 bits",
			event: SMFPDUSessionEstablishment{
				SUPI:         IMSI("262019876543210"),
				PDUSessionID: 5,
				GTPTunnelID:  FTEID{TEID: 4294967296, IPv4Address: IPv4Address{10, 250, 0, 1}},
			},
			want: "TEID is defined as INTEGER (0..4294967295)",
		},
		{
			// The FTEID address, which is the same named type as the UEEndpointAddress arm and
			// so inherits the same check.
			name: "an FTEID address whose length is not its family's",
			event: SMFPDUSessionEstablishment{
				SUPI:         IMSI("262019876543210"),
				PDUSessionID: 5,
				GTPTunnelID:  FTEID{TEID: 1, IPv6Address: IPv6Address{0x20, 0x01}},
			},
			want: "IPv6Address is defined as SIZE(16)",
		},
		{
			name: "a service type that is not one octet",
			event: AMFUEServiceAccept{
				UserIdentifiers:        sampleIdentifiers(),
				ServiceMessageIdentity: ServiceAcceptIdentity{0x01},
				ServiceType:            ServiceType{0x01, 0x02},
			},
			want: "ServiceType is defined as SIZE(1)",
		},
		{
			// A GUTI leaf. Every record carrying a GUTI carries all six, and all six come from
			// this element's own configuration — so a misconfigured PLMN reaches a record
			// without passing anything that would look at it.
			name: "an AMF set id above its range",
			event: AMFRegistration{
				RegistrationType:   RegTypeInitial,
				RegistrationResult: RegResult3GPPAccess,
				SUPI:               IMSI("262019876543210"),
				GUTI: FiveGGUTI{
					MCC: "262", MNC: "01", AMFRegionID: 1, AMFSetID: 1024, AMFPointer: 1, FiveGTMSI: 1,
				},
			},
			want: "AMFSetID is defined as INTEGER (0..1023)",
		},
		{
			name: "an MCC that is not three digits",
			event: AMFRegistration{
				RegistrationType:   RegTypeInitial,
				RegistrationResult: RegResult3GPPAccess,
				SUPI:               IMSI("262019876543210"),
				GUTI: FiveGGUTI{
					MCC: "2620", MNC: "01", AMFRegionID: 1, AMFSetID: 1, AMFPointer: 1, FiveGTMSI: 1,
				},
			},
			want: "MCC is defined as NumericString (SIZE(3))",
		},
		{
			// The alphabet, not the length. A NumericString carrying letters is the failure a
			// size check alone passes, and a PLMN read from configuration is where one arrives.
			name: "an MNC that is not digits",
			event: AMFRegistration{
				RegistrationType:   RegTypeInitial,
				RegistrationResult: RegResult3GPPAccess,
				SUPI:               IMSI("262019876543210"),
				GUTI: FiveGGUTI{
					MCC: "262", MNC: "0x", AMFRegionID: 1, AMFSetID: 1, AMFPointer: 1, FiveGTMSI: 1,
				},
			},
			want: "MNC is decimal digits throughout",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := EncodeXIRI(tc.event)
			if err == nil {
				t.Fatal("a record violating its own definition was encoded: a conformant mediation " +
					"function discards it, this element believes it delivered, and no fault is " +
					"raised on either side")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("refusal is %q, want it to name the field and the definition (%q)",
					err, tc.want)
			}
		})
	}

	// And a conformant record still encodes: the check must not be a way of emitting nothing.
	if _, err := EncodeXIRI(AMFUEPolicyTransfer{
		SUPI: IMSI("262019876543210"), UEPolicy: conformantPolicy,
	}); err != nil {
		t.Errorf("a conformant record was refused: %v", err)
	}

	// An enumerated member left unset is *absent*, not meaningless, and the constraint check
	// says nothing about it. Every enumeration here is numbered from one, so a member nobody
	// set reads as zero — and treating that as out of range would refuse every record that
	// legitimately omits an optional one. The direction matters: this check exists to stop a
	// value a *peer* chose from going out, and a peer's value is never the absence.
	//
	// Whether an absent member is an error at all belongs to the emitter, and this asserts the
	// division rather than assuming it: HandoverCause is mandatory in this record, so the
	// refusal arrives — in the emitter's words about a missing mandatory field, not in the
	// constraint check's about a value outside an enumeration.
	_, err := EncodeXIRI(AMFRANHandoverRequest{
		UserIdentifiers: sampleIdentifiers(),
		AMFUENGAPID:     1,
		RANUENGAPID:     2,
		HandoverType:    HandoverIntra5GS,
	})
	if err == nil {
		t.Error("a record omitting a mandatory member was encoded")
	} else if strings.Contains(err.Error(), "ENUMERATED") {
		t.Errorf("an unset member was refused as a value outside its enumeration (%v); zero is "+
			"absence, and every record that omits an optional enumerated member would fail", err)
	}
}

// TestAnAbsentOptionalLeafIsNotTooShort keeps the check from refusing every record that omits
// an optional field. An absent OPTIONAL leaf is a nil slice, an empty string or a zero
// structure, which the emitter omits — so it is "not present" rather than "present and too
// short", and treating them the same would make the validation itself the reason no record is
// delivered. Every leaf below is OPTIONAL in the module.
func TestAnAbsentOptionalLeafIsNotTooShort(t *testing.T) {
	for _, tc := range []struct {
		name  string
		event any
	}{
		{"an S-NSSAI with no slice differentiator", SMFPDUSessionModification{
			SNSSAI:        SNSSAI{SliceServiceType: 1},
			RequestType:   SMRequestModification,
			GTPTunnelInfo: someTunnelInfo,
		}},
		{"a tracking area with no network identifier", AMFRegistration{
			RegistrationType: RegTypeInitial, RegistrationResult: RegResult3GPPAccess,
			SUPI:          IMSI("262019876543210"),
			GUTI:          FiveGGUTI{MCC: "262", MNC: "01", AMFRegionID: 1, AMFSetID: 1, FiveGTMSI: 1},
			FiveGSTAIList: TAIList{{PLMNID: PLMNID{MCC: "262", MNC: "01"}, TAC: TAC{0x00, 0x01}}},
		}},
		{"an optional GUTI left out", AMFDeregistration{
			DeregistrationDirection: DirNetworkInitiated, AccessType: AccessThreeGPP,
		}},
		{"an optional PEI holding an empty IMEI, which the emitter omits", SMFPDUSessionRelease{
			SUPI: IMSI("262019876543210"), PEI: IMEI(""), PDUSessionID: 5,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := EncodeXIRI(tc.event); err != nil {
				t.Errorf("a record omitting an optional constrained leaf was refused: %v", err)
			}
		})
	}
}

// TestTheSUCIAndTAILeafConstraintsAreEnforced reaches the SUCI and TAI leaves directly,
// including through a pointer, which is how an optional SUCI member that may be zero is
// declared.
//
// A malformed SUCI is a wrong target identity in a well-formed record, so these are the
// constraints where a check that is registered but never reached would cost the most.
func TestTheSUCIAndTAILeafConstraintsAreEnforced(t *testing.T) {
	four := RoutingIndicatorLength(4)
	five := RoutingIndicatorLength(5)

	base := func() SUCI {
		return SUCI{
			MCC: "262", MNC: "01",
			RoutingIndicator:       1,
			ProtectionSchemeID:     0,
			HomeNetworkPublicKeyID: []byte{0x00},
			SchemeOutput:           []byte{0xDE, 0xAD},
			RoutingIndicatorLength: &four,
		}
	}

	for _, tc := range []struct {
		name  string
		value any
		want  string
	}{
		{"a routing indicator above its range", func() SUCI {
			s := base()
			s.RoutingIndicator = 10000

			return s
		}(), "RoutingIndicator is defined as INTEGER (0..9999)"},
		{"a protection scheme above its range", func() SUCI {
			s := base()
			s.ProtectionSchemeID = 16

			return s
		}(), "ProtectionSchemeID is defined as INTEGER (0..15)"},
		{"a routing-indicator length above its range, behind a pointer", func() SUCI {
			s := base()
			s.RoutingIndicatorLength = &five

			return s
		}(), "RoutingIndicatorLength is defined as INTEGER (1..4)"},
		{"a tracking area code that is too short", TAI{
			PLMNID: PLMNID{MCC: "262", MNC: "01"},
			TAC:    TAC{0x01},
		}, "TAC is defined as SIZE(2..3)"},
		{"a network identifier that is not eleven characters", TAI{
			PLMNID: PLMNID{MCC: "262", MNC: "01"},
			TAC:    TAC{0x01, 0x02},
			NID:    "tooshort",
		}, "NID is defined as"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateConstraints(tc.value)
			if err == nil {
				t.Fatalf("accepted a value its definition forbids; the constraint for this leaf "+
					"is registered but never reached (%+v)", tc.value)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("refusal does not name the leaf and its definition.\n got: %v\nwant substring: %s",
					err, tc.want)
			}
		})
	}

	// The conformant value must pass, or the cases above prove nothing.
	if err := validateConstraints(base()); err != nil {
		t.Errorf("a conformant SUCI was refused: %v", err)
	}
}

// TestAValueThatIsNoAlternativeIsRefusedBeforeItIsWalked: a CHOICE member is declared `any`, so
// it can hold any Go value, including one that contains itself. The constraint walk runs before
// the emitter, so it must refuse such a value by name, as the emitter would, rather than descend
// into it without end.
func TestAValueThatIsNoAlternativeIsRefusedBeforeItIsWalked(t *testing.T) {
	cyclic := []any{nil}
	cyclic[0] = cyclic

	registration := goldenSamples()["AMFRegistration"].(AMFRegistration)
	registration.SUPI = cyclic
	establishment := goldenSamples()["SMFPDUSessionEstablishment"].(SMFPDUSessionEstablishment)
	establishment.UEEndpoint = []any{IPv4Address{10, 0, 0, 1}, cyclic}

	for _, tc := range []struct {
		name  string
		event any
		want  string
	}{
		{
			"in a CHOICE member", registration,
			"AMFRegistration.SUPI holds []interface {}, which is not an alternative of the SUPI CHOICE",
		},
		{
			"in a list of CHOICE alternatives", establishment,
			"SMFPDUSessionEstablishment.UEEndpoint[1] holds []interface {}, which is not an alternative of the UEEndpointAddress CHOICE",
		},
		{
			"as the event", cyclic,
			"XIRIPayload.Event holds []interface {}, which is not an alternative of the XIRIEvent CHOICE",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := EncodeXIRI(tc.event)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("not refused as a non-alternative.\n got: %v\nwant substring: %s", err, tc.want)
			}
		})
	}
}

// TestTheWalksAlternativesAreTheModelledOnes: the walk refuses what choices does not list, and
// the emitters refuse what their switches do not take, so the two must agree. A type missing
// from choices fails the tag audit, which encodes every modelled alternative; this catches the
// other direction, a type choices lists that is not modelled.
func TestTheWalksAlternativesAreTheModelledOnes(t *testing.T) {
	if len(choices) != len(modelledAlternatives) {
		t.Errorf("choices has %d CHOICEs, and %d are modelled", len(choices), len(modelledAlternatives))
	}
	for name, c := range choices {
		modelled := map[reflect.Type]bool{}
		for _, typ := range modelledAlternatives[name] {
			modelled[typ] = true
		}
		listed := map[reflect.Type]bool{}
		for _, typ := range c.alternatives {
			listed[typ] = true
			if !modelled[typ] {
				t.Errorf("choices lists %s as an alternative of %s, which is not modelled", typ.Name(), c.name)
			}
		}
		for typ := range modelled {
			if !listed[typ] {
				t.Errorf("%s is a modelled alternative of %s that choices does not list", typ.Name(), c.name)
			}
		}
	}
}

// TestTheWalkReachesNoTypeThatContainsItself: the walk follows pointers, slices and CHOICE
// alternatives without bound, and finishes only because every type it can reach from a record
// ends. A field declared as an interface outside a CHOICE, or a type that reaches itself, would
// let a caller build a value the walk never leaves.
func TestTheWalkReachesNoTypeThatContainsItself(t *testing.T) {
	var visit func(typ reflect.Type, stack []reflect.Type, where string)
	visit = func(typ reflect.Type, stack []reflect.Type, where string) {
		if slices.Contains(stack, typ) {
			t.Errorf("%s: %s contains itself", where, typ.Name())
			return
		}
		stack = append(stack, typ)

		switch typ.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array:
			visit(typ.Elem(), stack, where)
		case reflect.Interface:
			t.Errorf("%s is declared %s outside a CHOICE, so the walk would follow whatever it holds", where, typ)
		case reflect.Struct:
			for i := range typ.NumField() {
				sf := typ.Field(i)
				if !sf.IsExported() {
					continue
				}
				w := where + "." + sf.Name
				name := choiceOf(sf)
				if name == "" {
					visit(sf.Type, stack, w)
					continue
				}
				c, ok := choices[name]
				if !ok {
					t.Errorf("%s is tagged choice:%s, which choices does not list", w, name)
					continue
				}
				for _, alt := range c.alternatives {
					visit(alt, stack, w+"("+alt.Name()+")")
				}
			}
		}
	}

	for _, record := range choices["xiriEvent"].alternatives {
		visit(record, nil, record.Name())
	}
}
