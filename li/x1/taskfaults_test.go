// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package x1

import (
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/omec-project/util/li/store"
	"github.com/omec-project/util/li/types"
)

const secondTestXID = types.XID("50b93d1e-1b53-4d63-aacb-e4d99811bc0c")

// faultSupplier records what it was asked about, so a test can assert the question as well as
// the answer.
type faultSupplier struct {
	mu     sync.Mutex
	asked  []types.XID
	faults map[types.XID][]X1Error
}

func (f *faultSupplier) supply(xid types.XID) []X1Error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, xid)

	return f.faults[xid]
}

func (f *faultSupplier) questions() []types.XID {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]types.XID(nil), f.asked...)
}

// twoTaskServer holds two tasks for one target, so an answer attributing a fault to one of them
// is distinguishable from an answer attributing it to whatever it is asked about.
func twoTaskServer(t *testing.T, opts ...Option) *Server {
	t.Helper()
	st := store.New()
	srv := NewServer(st, "neID", opts...)
	srv.now = func() time.Time { return zeroTailInstant }

	for _, xid := range []types.XID{testXID, secondTestXID} {
		if !st.Activate(types.InterceptTask{
			XID:      xid,
			Targets:  []types.TargetIdentifier{{Type: types.TargetSUPI, Value: "262019876543210"}},
			Products: []types.ProductType{types.ProductIRI},
			Deliveries: []types.DeliveryEndpoint{
				{Type: types.DeliveryX2, Address: "10.0.60.122:42069"},
			},
		}) {
			t.Fatalf("activate %s", xid)
		}
	}

	return srv
}

// answerTo runs one request and returns the rendered XML.
func answerTo(t *testing.T, srv *Server, msgType, body string) string {
	t.Helper()
	resp, err := srv.Process(request(msgType, body), admfPeer(t))
	if err != nil {
		t.Fatalf("Process %s: %v", msgType, err)
	}
	out, err := marshalResponse(resp)
	if err != nil {
		t.Fatalf("marshalResponse: %v", err)
	}

	return string(out)
}

// taskBlockFor returns the taskResponseDetails block naming xid, so an assertion about one
// task's faults cannot be satisfied by another task's.
func taskBlockFor(t *testing.T, answer string, xid types.XID) string {
	t.Helper()
	for _, block := range strings.Split(answer, "<ns1:taskResponseDetails>")[1:] {
		if end := strings.Index(block, "</ns1:taskResponseDetails>"); end >= 0 {
			block = block[:end]
		}
		if strings.Contains(block, string(xid)) {
			return block
		}
	}
	t.Fatalf("no taskResponseDetails names %s in:\n%s", xid, answer)

	return ""
}

// TestASuppliedTaskFaultIsAttributedToThatTask is the property the whole option exists for.
//
// An element that answers "provisioned, no faults" for a task the datapath has stopped
// carrying out is stating something true of provisioning and silent about operation, and the
// party with no other source is a triggering function: it tasks a POI over an interface the
// provisioning function cannot reach, so the POI's answer is the only account of that
// interception available to it. `triggerFaulty` exists to raise this against one warrant, which
// a function that cannot tell which warrant a fault concerned cannot do.
//
// So the assertion is attribution, not presence: two tasks are held, one is faulty, and the
// other's answer must stay empty. An element that returned the same list for every task would
// satisfy a presence check and be useless for the purpose.
func TestASuppliedTaskFaultIsAttributedToThatTask(t *testing.T) {
	sup := &faultSupplier{faults: map[types.XID][]X1Error{
		testXID: {{ErrorCode: issueCodeNonTerminatingFault, ErrorDescription: "the datapath refused the duplication rule for this task"}},
	}}
	srv := twoTaskServer(t, WithTaskFaults(sup.supply))

	for _, msgType := range []string{"GetAllTaskDetailsRequest", "GetAllDetailsRequest"} {
		t.Run(msgType, func(t *testing.T) {
			answer := answerTo(t, srv, msgType, "")

			faulty := taskBlockFor(t, answer, testXID)
			if !strings.Contains(faulty, "the datapath refused the duplication rule for this task") {
				t.Errorf("the faulty task answers with no fault, so the condition reaches the ADMF "+
					"at element scope and nothing says which warrant it concerned\ngot:\n%s", faulty)
			}

			cleared := taskBlockFor(t, answer, secondTestXID)
			if strings.Contains(cleared, "duplication rule") {
				t.Errorf("a task with nothing wrong with it answers with another task's fault, which "+
					"is the attribution defect in the other direction\ngot:\n%s", cleared)
			}
			if !strings.Contains(cleared, "<ns1:listOfFaults/>") {
				t.Errorf("a task with nothing wrong with it must answer an empty fault list, so a "+
					"fault list is evidence of a fault\ngot:\n%s", cleared)
			}
		})
	}

	// And the single-task answer, which renders through the same function two levels shallower.
	single := answerTo(t, srv, "GetTaskDetailsRequest", "\n    <ns1:xId>"+string(testXID)+"</ns1:xId>")
	if !strings.Contains(single, "the datapath refused the duplication rule for this task") {
		t.Errorf("GetTaskDetails reports no fault where GetAllDetails reports one; an ADMF that "+
			"asked two ways would have to decide which of its element's answers to believe\ngot:\n%s",
			single)
	}
}

// TestAnElementSupplyingNoTaskFaultsAnswersWithAnEmptyFaultList: registering no supplier is a
// legitimate configuration, and it produces an empty fault list — not an omitted element, and
// not a claim it has nothing to say about the task.
func TestAnElementSupplyingNoTaskFaultsAnswersWithAnEmptyFaultList(t *testing.T) {
	withNothing := answerTo(t, twoTaskServer(t), "GetAllTaskDetailsRequest", "")

	sup := &faultSupplier{faults: map[types.XID][]X1Error{}}
	withEmptySupplier := answerTo(t, twoTaskServer(t, WithTaskFaults(sup.supply)),
		"GetAllTaskDetailsRequest", "")

	if withNothing != withEmptySupplier {
		t.Errorf("a supplier that reports nothing produced a different answer from no supplier at "+
			"all:\nno supplier:\n%s\n\nempty supplier:\n%s", withNothing, withEmptySupplier)
	}
	if strings.Count(withNothing, "<ns1:listOfFaults/>") < 2 {
		t.Errorf("both tasks must still carry an empty listOfFaults, which is mandatory in "+
			"taskStatus\ngot:\n%s", withNothing)
	}
}

// TestATaskFaultAnswerNamesNoTarget: which task a fault concerns is already said by the answer
// it appears in, so naming the subject would add a target identity to an answer that does not
// need one — and the fault text is the one free-form field in it.
//
// The supplier's signature is the structural half of this: it is handed an XID and nothing else,
// so it has no target identity available to put in an answer even by mistake. Asserted rather
// than assumed, because a signature is easy to widen and the widening would look like a
// convenience.
func TestATaskFaultAnswerNamesNoTarget(t *testing.T) {
	const supi = "262019876543210"

	sup := &faultSupplier{faults: map[types.XID][]X1Error{
		testXID: {{ErrorCode: issueCodeNonTerminatingFault, ErrorDescription: "the trigger for this task names a session that has gone"}},
	}}
	srv := twoTaskServer(t, WithTaskFaults(sup.supply))

	answer := answerTo(t, srv, "GetAllTaskDetailsRequest", "")
	block := taskBlockFor(t, answer, testXID)

	// The task block names the target in its own targetIdentifiers, which is what the block is
	// for; the fault must not.
	status := block[strings.Index(block, "<ns1:taskStatus>"):]
	if strings.Contains(status, supi) {
		t.Errorf("the task's fault names the subject, putting a target identity into an answer "+
			"about a condition\ngot:\n%s", status)
	}

	// The supplier was asked about the tasks in the answer, by XID, and about nothing else.
	asked := sup.questions()
	if len(asked) == 0 {
		t.Fatal("the supplier was never asked, so this test asserts nothing about what it was asked")
	}
	for _, xid := range asked {
		if xid != testXID && xid != secondTestXID {
			t.Errorf("the supplier was asked about %q, which is neither task this element holds", xid)
		}
	}
}

// TestAnElementScopedConditionIsNotAttributedToATask is the other direction of the scoping,
// and the one an element gets wrong by being helpful.
//
// TS 103 221-1 separates the two levels explicitly: NE status is "OK" or "Faults i.e. NE losing
// traffic", and those are "separate from delivery faults which are reported per XID". A
// condition that concerns the element — its mediation functions unreachable, its egress down —
// is true of every task it holds, so an element that answered it against each task would report
// one fault N times and imply each warrant was separately broken. The ADMF then cannot tell
// "this element is losing traffic" from "N of my warrants are not arriving", which is the
// distinction that decides what it does next.
//
// Asserted with a real element-scoped fault present, because the interesting case is not an
// element with nothing to say.
func TestAnElementScopedConditionIsNotAttributedToATask(t *testing.T) {
	const condition = "every mediation function this element delivers to is unreachable"

	sup := &faultSupplier{faults: map[types.XID][]X1Error{}}
	srv := twoTaskServer(t,
		WithTaskFaults(sup.supply),
		WithFaultProbes(func() *X1Error { return NEFault(NEIssueMDFUnreachable, condition) }),
	)

	answer := answerTo(t, srv, "GetAllDetailsRequest", "")

	// The element says it, once, where the schema puts an element's own conditions.
	if !strings.Contains(answer, condition) {
		t.Fatalf("the element's own status does not carry the condition its probe reports, so this "+
			"test would pass against an element that reports nothing anywhere\ngot:\n%s", answer)
	}
	neStatus := answer[:strings.Index(answer, "<ns1:listOfTaskResponseDetails>")]
	if !strings.Contains(neStatus, condition) {
		t.Errorf("the condition is not in neStatusDetails, which is where an element's own faults "+
			"belong\ngot:\n%s", neStatus)
	}

	// And no task answers it.
	for _, xid := range []types.XID{testXID, secondTestXID} {
		block := taskBlockFor(t, answer, xid)
		if strings.Contains(block, condition) {
			t.Errorf("task %s answers an element-scoped condition. One fault reported once per "+
				"warrant reads as N broken warrants, and an ADMF cannot tell that from an element "+
				"losing traffic\ngot:\n%s", xid, block)
		}
		if !strings.Contains(block, "<ns1:listOfFaults/>") {
			t.Errorf("task %s does not answer an empty fault list while nothing task-scoped is "+
				"wrong with it\ngot:\n%s", xid, block)
		}
	}
}

// TestConditionScopesAreDisjoint is what makes "the choice is made once" enforceable.
//
// A condition reported at both scopes is one an ADMF sees twice and cannot reconcile: it has to
// decide whether one element is losing traffic or N of its warrants are separately broken, and
// the two need different responses. A condition in neither table is one nothing can classify,
// which is how a per-author decision gets made again at the next call site.
func TestConditionScopesAreDisjoint(t *testing.T) {
	if len(taskScoped) == 0 {
		t.Fatal("no task-scoped conditions are declared, so this test asserts nothing")
	}
	for condition := range taskScoped {
		if _, alsoElement := neIssueEncodings[condition]; alsoElement {
			t.Errorf("%q is declared at both element and task scope; an ADMF receiving it twice "+
				"cannot tell one element losing traffic from several warrants separately broken",
				condition)
		}
	}

	// And every declared NEIssue constant is element-scoped, which is the other direction: a
	// condition added to report.go and to neither table would be classified by whichever call
	// site reached it first.
	for _, condition := range declaredNEIssues(t) {
		if taskScoped[condition] {
			t.Errorf("%q is declared as an NE issue and as task-scoped", condition)
		}
	}
}

// TestATaskFaultMustNameADeclaredCondition: the constructor is where the scope decision is
// enforced, because it is the one place every supplier's answer passes through.
func TestATaskFaultMustNameADeclaredCondition(t *testing.T) {
	good := TaskFault(TaskIssueNoTrafficSelected, "nothing selects this task's traffic")
	if !strings.HasPrefix(good.ErrorDescription, TaskIssueNoTrafficSelected+": ") {
		t.Errorf("a declared condition does not lead its description: %q", good.ErrorDescription)
	}
	if good.ErrorCode == 0 {
		t.Error("a task fault carries no issue code, so it cannot be correlated with a pushed report")
	}

	// An element-scoped condition, answered against a task. Refused, and the refusal says whose
	// defect it is: a description naming the element rather than the task is the honest answer
	// when the element has misclassified its own condition.
	bad := TaskFault(NEIssueMDFUnreachable, "every mediation function is unreachable")
	if strings.Contains(bad.ErrorDescription, "unreachable") {
		t.Errorf("an element-scoped condition was carried into a task's answer: %q",
			bad.ErrorDescription)
	}
	if !strings.Contains(bad.ErrorDescription, "does not declare as task-scoped") {
		t.Errorf("the refusal does not say what went wrong: %q", bad.ErrorDescription)
	}
}

// TestTheFaultSupplierIsAskedOnlyForAnswersThatReportFaults: the supplier may walk every
// session the element holds, once per task. ListAllDetails names tasks by XID and renders no
// fault, so asking it there would be work done and thrown away — but the answers that do render
// faults must still ask, or they would report a faulty task as healthy.
func TestTheFaultSupplierIsAskedOnlyForAnswersThatReportFaults(t *testing.T) {
	for msgType, wantAsked := range map[string]bool{
		"ListAllDetailsRequest":    false,
		"GetAllTaskDetailsRequest": true,
		"GetAllDetailsRequest":     true,
	} {
		sup := &faultSupplier{}
		answerTo(t, twoTaskServer(t, WithTaskFaults(sup.supply)), msgType, "")

		if asked := len(sup.questions()) > 0; asked != wantAsked {
			t.Errorf("%s: the fault supplier was asked=%v, want %v", msgType, asked, wantAsked)
		}
	}
}

// TestEveryRendererThatReportsTaskFaultsIsListed keeps rendersTaskFaults in step with
// responseBody: a renderer that reports faults but is missing from the list would answer every
// task as faultless, and no test of the renderer itself would notice. Every response type is
// rendered with a marked fault, and any that prints it must be listed.
func TestEveryRendererThatReportsTaskFaultsIsListed(t *testing.T) {
	src, err := os.ReadFile("response.go")
	if err != nil {
		t.Fatal(err)
	}

	body := string(src)
	start := strings.Index(body, "func responseBody(")
	if start < 0 {
		t.Fatal("cannot find responseBody")
	}
	end := strings.Index(body[start:], "\n}\n")

	const marker = "a fault only the renderer under test could print"
	faults := map[types.XID][]X1Error{testXID: {{ErrorCode: 9020, ErrorDescription: marker}}}

	seen := 0
	for _, line := range strings.Split(body[start:start+end], "\n") {
		label, ok := strings.CutPrefix(strings.TrimSpace(line), "case \"")
		if !ok {
			continue
		}
		typ := strings.TrimSuffix(label, "\":")
		seen++

		m := X1ResponseMessage{
			Type:       typ,
			Tasks:      []types.InterceptTask{{XID: testXID}},
			TaskFaults: faults,
		}
		if strings.Contains(responseBody(m), marker) && !rendersTaskFaults(typ) {
			t.Errorf("%s renders task faults but rendersTaskFaults does not list it", typ)
		}
	}

	if seen == 0 {
		t.Fatal("no response types were found in responseBody, so nothing was checked")
	}
}
