package extmsg

import "testing"

func TestEditReplyPartsRoundTrip(t *testing.T) {
	detail := WithAlsoEditing(WithStale(WithVersion(ReadSourceDetail("S"), "0123456789ab"), "a layer below changed"), "repoA", "T (a); U (b)")
	if session, staged := StagedSessionIn(detail); !staged || session != "S" {
		t.Fatalf("staged session: %q %v", session, staged)
	}
	if version := VersionIn(detail); version != "0123456789ab" {
		t.Fatalf("version: %q", version)
	}
	if stale, found := StaleIn(detail); !found || stale != "a layer below changed" {
		t.Fatalf("stale: %q %v", stale, found)
	}
	if repo, sessions, found := AlsoEditingIn(detail); !found || repo != "repoA" || sessions != "T (a); U (b)" {
		t.Fatalf("also editing: %q %q %v", repo, sessions, found)
	}
	if _, staged := StagedSessionIn(ReadSourceDetail("")); staged {
		t.Fatal("the disk is no session")
	}
	if session, staged := StagedReplySession(StagedReply("S")); !staged || session != "S" {
		t.Fatalf("staged reply: %q %v", session, staged)
	}
	if _, staged := StagedReplySession(OkReply("not staged")); staged {
		t.Fatal("not staged names no session")
	}
}

func TestStagedReplySession(t *testing.T) {
	for _, line := range []string{
		ErrorReply("path outside the project: ../notes.md"),
		OkReply("not staged"),
		"ok " + stagedMarker,
		"",
	} {
		t.Run("should name no session when the reply is "+line, func(t *testing.T) {
			if session, staged := StagedReplySession(line); staged || session != "" {
				t.Fatalf("got %q %v", session, staged)
			}
		})
	}
	t.Run("should name the session when the reply is a staged reply", func(t *testing.T) {
		if session, staged := StagedReplySession(StagedReply("A")); !staged || session != "A" {
			t.Fatalf("got %q %v", session, staged)
		}
	})
}

func TestTheEditorAnswersAnEndBeforeItsSenderGivesUp(t *testing.T) {
	if EditorEndWait >= EditEndTimeout {
		t.Fatalf("the editor holds an end %v, the sender waits %v: the sender would time out first", EditorEndWait, EditEndTimeout)
	}
}
