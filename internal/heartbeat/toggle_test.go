package heartbeat

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bigknoxy/joshbot/internal/bus"
)

func writeHeartbeat(t *testing.T, content string) (*Service, *bus.MessageBus, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "HEARTBEAT.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	b := bus.NewMessageBus()
	return NewService(b, dir), b, path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// The toggle and the tick share one regex: every line the tick can check off,
// the toggle can re-open, and the re-opened line publishes again.
func TestParseLineRoundTripsWithParseTask(t *testing.T) {
	for _, line := range []string{"- [ ] task", "*  [ ]task", "\t+ [ ] tabbed", "- [ ] crlf\r"} {
		_, checked, ok := parseTask(line)
		if !ok {
			t.Fatalf("parseTask(%q) should match", line)
		}
		text, done, reopened, ok := parseLine(checked)
		if !ok || !done {
			t.Fatalf("parseLine(%q) should see a done task", checked)
		}
		if reopened != line {
			t.Errorf("re-opening %q gave %q, want the original %q", checked, reopened, line)
		}
		if again, _, ok := parseTask(reopened); !ok || again != text {
			t.Errorf("re-opened %q does not publish as %q", reopened, text)
		}
	}
	if _, done, _, ok := parseLine("* [X] upper"); !ok || !done {
		t.Error("[X] is a done task")
	}
	for _, line := range []string{"# Heading", "[ ] no bullet", "- [ ]", "- [y] other"} {
		if _, _, _, ok := parseLine(line); ok {
			t.Errorf("parseLine(%q) should not match", line)
		}
	}
}

func TestListTasks(t *testing.T) {
	svc, _, _ := writeHeartbeat(t, "# Tasks\n- [ ] water plants\nnotes\n* [x] pay rent\n")
	tasks, err := svc.Tasks()
	if err != nil {
		t.Fatal(err)
	}
	want := []Task{
		{Line: 1, Key: TaskKey("water plants"), Text: "water plants", Done: false},
		{Line: 3, Key: TaskKey("pay rent"), Text: "pay rent", Done: true},
	}
	if len(tasks) != len(want) {
		t.Fatalf("tasks = %+v", tasks)
	}
	for i := range want {
		if tasks[i] != want[i] {
			t.Errorf("task %d = %+v, want %+v", i, tasks[i], want[i])
		}
	}
	if len(TaskKey("x")) != 8 {
		t.Error("TaskKey must be 8 hex characters to fit callback_data")
	}
	missing, err := ListTasks(filepath.Join(t.TempDir(), "HEARTBEAT.md"))
	if err != nil || missing != nil {
		t.Errorf("a missing file is no tasks, got %v, %v", missing, err)
	}
}

func TestToggleFlipsBothWaysInPlace(t *testing.T) {
	svc, _, path := writeHeartbeat(t, "# Tasks\r\n  - [ ]  water plants\r\n* [x] pay rent\r\n")
	tasks, err := svc.Toggle(1, TaskKey("water plants"), false)
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); got != "# Tasks\r\n  - [x]  water plants\r\n* [x] pay rent\r\n" {
		t.Errorf("after closing: %q", got)
	}
	if !tasks[0].Done {
		t.Error("returned tasks should reflect the write")
	}
	if _, err := svc.Toggle(2, TaskKey("pay rent"), true); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); got != "# Tasks\r\n  - [x]  water plants\r\n* [ ] pay rent\r\n" {
		t.Errorf("after re-opening: %q", got)
	}
}

// A press drawn from an older file must not flip whatever now sits on the
// line it names.
func TestToggleRefusesAStalePress(t *testing.T) {
	svc, _, path := writeHeartbeat(t, "- [ ] a\n- [ ] b\n")
	before := readFile(t, path)
	for _, tc := range []struct {
		line int
		key  string
	}{
		{0, TaskKey("b")}, // line 0 holds a different task now
		{5, TaskKey("a")}, // past the end
		{-1, TaskKey("a")},
		{0, ""},
	} {
		if _, err := svc.Toggle(tc.line, tc.key, false); !errors.Is(err, ErrTaskChanged) {
			t.Errorf("Toggle(%d, %q) err = %v, want ErrTaskChanged", tc.line, tc.key, err)
		}
	}
	if readFile(t, path) != before {
		t.Error("a refused toggle must not write")
	}
}

// Re-opening a task this process already published must let it run again:
// without clearing the published set the next tick would check it straight
// back off and never deliver it.
func TestToggleReopenedTaskPublishesAgain(t *testing.T) {
	svc, b, path := writeHeartbeat(t, "- [ ] check the disk\n")
	svc.scanAndPublish()
	if n := len(drainInbound(b)); n != 1 {
		t.Fatalf("first tick published %d, want 1", n)
	}
	if !strings.Contains(readFile(t, path), "[x]") {
		t.Fatal("the tick should have checked it off")
	}
	if _, err := svc.Toggle(0, TaskKey("check the disk"), true); err != nil {
		t.Fatal(err)
	}
	svc.scanAndPublish()
	if n := len(drainInbound(b)); n != 1 {
		t.Fatalf("re-opened task published %d times on the next tick, want 1", n)
	}
}

// Toggle and the tick serialise on one lock, so a toggle cannot be lost to a
// tick that read the file before it. Run under -race.
func TestToggleAndTickDoNotLoseWrites(t *testing.T) {
	svc, b, path := writeHeartbeat(t, "* [x] done one\n")
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); svc.scanAndPublish() }()
		go func() { defer wg.Done(); _, _ = svc.Toggle(0, TaskKey("done one"), true) }()
	}
	wg.Wait()
	drainInbound(b)
	if _, _, _, ok := parseLine(strings.TrimSpace(readFile(t, path))); !ok {
		t.Errorf("file corrupted: %q", readFile(t, path))
	}
}

// A tap means what the user saw. A button drawn while a task was open must not
// re-open it after the tick has run and checked it off, and the second callback
// of a double tap must not flip the task straight back (review of #411).
func TestToggleRefusesAPressWhoseStateIsStale(t *testing.T) {
	svc, b, path := writeHeartbeat(t, "- [ ] send report\n")
	// The keyboard showed it open; the tick runs and checks it off first.
	svc.scanAndPublish()
	if n := len(drainInbound(b)); n != 1 {
		t.Fatalf("tick published %d, want 1", n)
	}
	checkedOff := readFile(t, path)
	if _, err := svc.Toggle(0, TaskKey("send report"), false); !errors.Is(err, ErrTaskChanged) {
		t.Fatalf("press drawn as open, file now done: err = %v, want ErrTaskChanged", err)
	}
	if readFile(t, path) != checkedOff {
		t.Error("the refused press re-opened the task")
	}
	svc.scanAndPublish()
	if n := len(drainInbound(b)); n != 0 {
		t.Errorf("the task ran again: %d publishes", n)
	}

	// Double tap on one keyboard: both callbacks carry the same shown state.
	svc2, _, path2 := writeHeartbeat(t, "* [x] pay rent\n")
	if _, err := svc2.Toggle(0, TaskKey("pay rent"), true); err != nil {
		t.Fatal(err)
	}
	if _, err := svc2.Toggle(0, TaskKey("pay rent"), true); !errors.Is(err, ErrTaskChanged) {
		t.Fatalf("second tap of a double tap: err = %v, want ErrTaskChanged", err)
	}
	if got := readFile(t, path2); got != "* [ ] pay rent\n" {
		t.Errorf("after a double tap: %q, want it re-opened exactly once", got)
	}
}

// The summary is the text the keyboard rides and every toggle edits, so it
// must fit one Telegram message, counted in UTF-16 units as Telegram counts.
func TestSummaryFitsOneTelegramMessage(t *testing.T) {
	var tasks []Task
	for i := 0; i < 200; i++ {
		tasks = append(tasks, Task{Text: strings.Repeat("😀", 30)}) // 2 UTF-16 units each
	}
	got := Summary(tasks)
	if n := utf16Len(got); n > summaryBudget {
		t.Fatalf("summary is %d UTF-16 units, over the %d budget", n, summaryBudget)
	}
	if !strings.Contains(got, "more in HEARTBEAT.md") {
		t.Error("a cut summary must say how many tasks are not shown")
	}
	shown := strings.Count(got, "\n☐ ")
	if !strings.Contains(got, fmt.Sprintf("…and %d more", 200-shown)) {
		t.Errorf("the 'more' count does not match the tasks left out (shown %d)", shown)
	}
	// A short list is shown whole, with no "more" line.
	if short := Summary(tasks[:3]); strings.Contains(short, "more in") || strings.Count(short, "\n☐ ") != 3 {
		t.Errorf("short summary = %q", short)
	}
}

func TestSummary(t *testing.T) {
	if got := Summary(nil); !strings.Contains(got, "No heartbeat tasks") {
		t.Errorf("empty summary = %q", got)
	}
	got := Summary([]Task{{Text: "a"}, {Text: "b", Done: true}})
	for _, want := range []string{"1 open, 1 done", "\n☐ a", "\n✅ b"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary %q missing %q", got, want)
		}
	}
}
