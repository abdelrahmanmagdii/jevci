package gitdiff

import (
	"reflect"
	"testing"
)

func TestParseHunks(t *testing.T) {
	diff := `diff --git a/pkg/a.go b/pkg/a.go
index 111..222 100644
--- a/pkg/a.go
+++ b/pkg/a.go
@@ -1,3 +1,4 @@ func a() {
 context
+added
@@ -10 +12,2 @@
-old
+new1
+new2
diff --git a/pkg/deleted.go b/pkg/deleted.go
deleted file mode 100644
index 111..000
--- a/pkg/deleted.go
+++ /dev/null
@@ -1,5 +0,0 @@
-line
diff --git a/pkg/new.go b/pkg/new.go
new file mode 100644
index 000..111
--- /dev/null
+++ b/pkg/new.go
@@ -0,0 +1,3 @@
+line
diff --git a/pkg/old.go b/pkg/renamed.go
similarity index 90%
rename from pkg/old.go
rename to pkg/renamed.go
--- a/pkg/old.go
+++ b/pkg/renamed.go
@@ -5 +5 @@
diff --git a/pkg/zero.go b/pkg/zero.go
--- a/pkg/zero.go
+++ b/pkg/zero.go
@@ -3,2 +10,0 @@
`
	got := ParseHunks(diff)
	want := map[string][]Hunk{
		"pkg/a.go": {
			{OldStart: 1, OldLines: 3, NewStart: 1, NewLines: 4},
			{OldStart: 10, OldLines: 1, NewStart: 12, NewLines: 2},
		},
		"pkg/deleted.go": {
			{OldStart: 1, OldLines: 5, NewStart: 0, NewLines: 0},
		},
		"pkg/new.go": {
			{OldStart: 0, OldLines: 0, NewStart: 1, NewLines: 3},
		},
		"pkg/renamed.go": {
			{OldStart: 5, OldLines: 1, NewStart: 5, NewLines: 1},
		},
		"pkg/zero.go": {
			{OldStart: 3, OldLines: 2, NewStart: 10, NewLines: 0},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
}
