// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"crypto/x509"
	"encoding/asn1"
	"strings"
)

// X1 error codes (TS 103 221-1 table 6.7-3) used by peer authentication.
const (
	errCodeADMFCertMismatch   = 1030 // ADMF Identifier does not match certificate details
	errCodeUnexpectedADMF     = 1040 // Unexpected ADMF Identifier
	errCodeUnexpectedNE       = 1060 // Unexpected NE Identifier
	errCodeGeneric            = 1000 // Generic error
	errCodeUnsupportedRequest = 1080 // Unsupported request
	errCodeUnsupportedVersion = 1020 // Unsupported version
	// 1010 covers a value that does not conform to the format the schema defines for it
	// — a dId that is not a UUID, say. It names what is wrong, where the
	// per-operation generic codes name only where it happened.
	errCodeSchemaError = 1010 // Syntax/schema error
	// 2020 per table 6.7-3, not 1020 ("Unsupported version"): a wrong code is as unhelpful
	// to an ADMF as an invented one.
	errCodeNoSuchTask = 2020 // XID does not exist on NE
	// 2010 answers an ActivateTask naming a held XID with a different task; an identical
	// replay is acknowledged instead.
	errCodeXIDExists = 2010 // XID already exists on NE
	// Destination and bulk-operation codes, from the same table. A generic 1000 in place of
	// one of these is not a small matter: an ADMF distinguishes "the DID I named is not
	// there" from "something went wrong" by the code, and only the first tells it what to do.
	errCodeDIDExists    = 2030 // DID already exists on the NE
	errCodeNoSuchDID    = 2040 // DID does not exist on the NE
	errCodeDeactAllOff  = 5010 // DeactivateAllTasks not enabled
	errCodeRemoveAllOff = 8020 // RemoveAllDestinations not enabled
	// 8010 is the specific refusal for the guard the specification puts on bulk removal,
	// and it carries information a generic 1000 does not: the table's own suggested
	// content is "details of which Destinations are in use, and (if possible) by which
	// Tasks", so an ADMF reading it knows to deactivate tasking before retrying rather
	// than to investigate a failure.
	errCodeDestinationsInUse = 8010 // Destinations in use
	// The task-provisioning codes. 3000/3001 are the registry's own "details of why the
	// Task cannot be activated/modified", which is what a refusal on a field this
	// element cannot honour is; 3050 names the ServiceType refusal exactly.
	errCodeActivateFailed = 3000 // Generic ActivateTask failure
	errCodeModifyFailed   = 3001 // Generic ModifyTask failure
	errCodeBadServiceType = 3050 // Unsupported ServiceType
	// The narrower entries of the same group, used wherever the reason is one of theirs:
	// "implementers shall use the most specific error code available".
	errCodeUnsupportedTarget      = 3010 // Unsupported TargetIdentifier type
	errCodeBadTargetCombination   = 3020 // Unsupported combination of TargetIdentifiers
	errCodeBadDeliveryCombination = 3040 // Invalid combination of DeliveryType and Destinations specified
	// 6000 is the destination equivalent of 3000: "details of why the Destination cannot
	// be created". Table 6.7-3 offers nothing narrower for a DID this element will not
	// redefine, and 7010 "Destination in use" belongs to RemoveDestination — a code from
	// the wrong operation's group is as unhelpful to an ADMF as an invented one.
	errCodeCreateDestFailed = 6000 // Generic CreateDestination failure
	// 6020 is the destination counterpart: a deliveryAddress given as a URI, an E.164
	// number or an email address rather than an IP address and port.
	errCodeBadAddressType = 6020 // Unsupported DeliveryAddress type
)

// roleADMF is the role a peer tasking this network element presents in its
// certificate binding URN. TS 103 221-1 annex G (table G.2-1) defines "ADMF" and
// "NE"; an NE checks its peer against the former, and presents the latter itself.
const roleADMF = "ADMF"

// roleNE is the other side of the same table: the role a network element presents,
// and therefore the role a *requester* checks when it is the one being answered. A
// triggering function tasking a triggered POI is the ADMF to that POI's NE, so the
// answer it receives is bound in this role.
const roleNE = "NE"

// certBindingURNPrefix is the ETSI TC LI namespace prefix of a certificate
// binding URN: urn:etsi:li:103221-1:cert-binding:{role}:{identifier}.
const certBindingURNPrefix = "urn:etsi:li:103221-1:cert-binding:"

// oidUID is the UID relative distinguished name (IETF RFC 4519 section 2.39),
// one of the two places TS 103 221-1 clause 8.2.4 allows a peer's X1 identifier
// to be bound into its certificate.
var oidUID = asn1.ObjectIdentifier{0, 9, 2342, 19200300, 100, 1, 1}

// certBinds reports whether cert carries identifier for the given X1 role, per
// TS 103 221-1 clause 8.2.4: either the Subject contains a UID relative
// distinguished name equal to identifier, or a subjectAltName of type
// uniformResourceIdentifier holds the matching annex G certificate binding URN.
// Either form suffices ("either or both of the following conditions are true").
func certBinds(cert *x509.Certificate, role, identifier string) bool {
	if cert == nil || identifier == "" {
		return false
	}
	for _, n := range cert.Subject.Names {
		if !n.Type.Equal(oidUID) {
			continue
		}
		if v, ok := n.Value.(string); ok && v == identifier {
			return true
		}
	}
	// Annex G G.3: the URN is valid only if its format, role, and identifier all
	// match, which an exact comparison against the expected value enforces.
	want := certBindingURNPrefix + role + ":" + identifier
	for _, u := range cert.URIs {
		if u != nil && u.String() == want {
			return true
		}
	}
	return false
}

// certUID returns the UID relative distinguished name a peer's certificate
// carries, or "" if it carries none.
//
// certBinds answers "does this certificate bind *this* identifier", which is the
// question authentication asks. This answers "who does it say it is", which is
// the question clause 6.1 asks when a request could not be parsed and the ADMF
// identifier has to come from somewhere other than the message.
func certUID(cert *x509.Certificate) string {
	if cert == nil {
		return ""
	}
	for _, n := range cert.Subject.Names {
		if !n.Type.Equal(oidUID) {
			continue
		}
		if v, ok := n.Value.(string); ok {
			return v
		}
	}

	return ""
}

// certIdentifier returns the X1 identifier cert carries for the given role, from
// either of the two forms clause 8.2.4 defines: the Subject UID relative
// distinguished name, or an annex G certificate binding URI.
//
// certBinds answers "does this certificate bind *this* identifier", which is the
// question authentication asks. certUID answers "who does the Subject say it is",
// which is what clause 6.1 needs when a request could not be parsed. This is the
// second question asked of both forms, and it exists because answering it from the
// Subject alone cannot name a peer whose identity is carried only by the URI — a form
// certBinds accepts for authentication — and an answer naming nobody is one that peer
// cannot attribute to itself.
//
// The role is part of the question because the URN carries it: a certificate binding
// an identifier as an ADMF says nothing about that identifier as an NE.
func certIdentifier(cert *x509.Certificate, role string) string {
	if uid := certUID(cert); uid != "" {
		return uid
	}
	if cert == nil {
		return ""
	}

	prefix := certBindingURNPrefix + role + ":"
	for _, u := range cert.URIs {
		if u == nil {
			continue
		}
		if id, found := strings.CutPrefix(u.String(), prefix); found && id != "" {
			return id
		}
	}

	return ""
}

// authenticate applies TS 103 221-1 clause 8.2.4 to one request message: a valid
// TLS handshake is not on its own sufficient authentication, because every peer
// holding any certificate from the LI CA would then be able to task this network
// element under any asserted identity. The identifier the message asserts must
// also be bound into the certificate the peer presented.
//
// It returns a non-zero X1 error code, and a description safe to return to the
// ADMF, when the message must be rejected. Nothing is logged: an authentication
// failure is reported over X1 only (undetectability).
func (s *Server) authenticate(m X1RequestMessage, peer *x509.Certificate) (int, string) {
	if peer == nil {
		return errCodeADMFCertMismatch, "no client certificate presented"
	}
	if m.AdmfIdentifier == "" {
		return errCodeADMFCertMismatch, "missing admfIdentifier"
	}
	// The binding proves the asserted identity is the certified one; it does not
	// say that identity may task this NE. When the responsible ADMF is configured,
	// hold the peer to it, so a certified-but-unauthorized peer is refused.
	if s.admfID != "" && m.AdmfIdentifier != s.admfID {
		return errCodeUnexpectedADMF, "unexpected ADMF identifier"
	}
	// A request addressed to a different network element was misrouted; applying it
	// here would provision tasking the ADMF believes lives elsewhere. An absent
	// identifier is refused on the same grounds rather than waved through: the
	// schema makes neIdentifier mandatory, so a request without one carries no
	// evidence it was meant for this element at all.
	if m.NeIdentifier != s.neID {
		return errCodeUnexpectedNE, "unexpected NE identifier"
	}
	if !certBinds(peer, roleADMF, m.AdmfIdentifier) {
		return errCodeADMFCertMismatch, "ADMF identifier does not match certificate details"
	}
	return 0, ""
}
