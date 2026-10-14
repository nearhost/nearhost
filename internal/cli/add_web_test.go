package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

const askWeb = "Which one is the web app?\n" +
	"  1) forward-db (FORWARD_DB_PORT)  3306\n" +
	"  2) vite (VITE_PORT)              5173\n" +
	"  3) app (APP_PORT)                8080\n" +
	"  0) none\n" +
	"Choose [1-3, 0]: "

// webAsking is an add backend with a terminal, a config path and a project
// whose .env holds three port variables and nothing that says which is web.
func webAsking(t *testing.T, terminal bool) (string, string, addBackend) {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	b := noRemote(t)
	b.interactive = func() bool { return terminal }
	return cfgPath, envProject(t, "APP_PORT=8080\nFORWARD_DB_PORT=3306\nVITE_PORT=5173\n"), b
}

func TestAddAsksWhichRouteIsWeb(t *testing.T) {
	cfgPath, dir, b := webAsking(t, true)
	out, err := executeWithInput(t, "3\n", newAddCmd(b), "--config", cfgPath, "add", dir, "--name", "envapp")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	wantContains(t, out, askWeb, "  8080  web     app (APP_PORT)\n", "  3306  ignore  forward-db (FORWARD_DB_PORT)\n", "Added envapp on local")
	p, _ := loadSaved(t, cfgPath).Project("envapp")
	if web, ok := p.WebRoute(); !ok || web.Port != 8080 {
		t.Fatalf("routes = %+v", p.Routes)
	}
}

func TestAddAsksAgainUntilTheAnswerIsInRange(t *testing.T) {
	cfgPath, dir, b := webAsking(t, true)
	out, err := executeWithInput(t, "4\nvite\n-1\n 2 \n", newAddCmd(b), "--config", cfgPath, "add", dir, "--name", "envapp")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	if n := strings.Count(out, "Choose [1-3, 0]: "); n != 4 {
		t.Errorf("asked %d times, want 4:\n%s", n, out)
	}
	if strings.Count(out, "Which one is the web app?") != 1 {
		t.Errorf("the list should be shown once:\n%s", out)
	}
	wantContains(t, out, "  5173  web     vite (VITE_PORT)\n")
}

// None, chosen or by the end of input, leaves every route ignored, and a
// project needs a web route, so nothing is saved.
func TestAddWithNoWebRouteChosenSavesNothing(t *testing.T) {
	for name, input := range map[string]string{"typed 0": "0\n", "no answer at all": ""} {
		t.Run(name, func(t *testing.T) {
			cfgPath, dir, b := webAsking(t, true)
			out, err := executeWithInput(t, input, newAddCmd(b), "--config", cfgPath, "add", dir, "--name", "envapp")
			if err == nil || !strings.Contains(err.Error(), "nothing was saved") {
				t.Fatalf("expected nothing to be saved, got %v\n%s", err, out)
			}
			wantContains(t, out, askWeb, "  8080  ignore  app (APP_PORT)\n")
			if strings.Count(out, "Choose [1-3, 0]: ") != 1 {
				t.Errorf("an answer of none must not be asked again:\n%s", out)
			}
			if _, ok := loadSaved(t, cfgPath).Project("envapp"); ok {
				t.Error("the project was saved")
			}
		})
	}
}

func TestAddWithoutATerminalSaysHowToPickTheWebRoute(t *testing.T) {
	cfgPath, dir, b := webAsking(t, false)
	// Input is waiting, and must not be read as an answer.
	out, err := executeWithInput(t, "3\n", newAddCmd(b), "--config", cfgPath, "add", dir, "--name", "envapp")
	if err == nil {
		t.Fatalf("add succeeded:\n%s", out)
	}
	wantContains(t, out, "  8080  ignore  app (APP_PORT)\n")
	wantContains(t, err.Error(), "Run it again with --web <port>")
	if strings.Contains(out, "No web route") {
		t.Errorf("the error alone says how to choose:\n%s", out)
	}
	if strings.Contains(out, "Which one") {
		t.Errorf("nothing may be asked without a terminal:\n%s", out)
	}
}

func TestAddWebFlagSkipsTheWebQuestion(t *testing.T) {
	cfgPath, dir, b := webAsking(t, true)
	out, err := executeWithInput(t, "1\n", newAddCmd(b), "--config", cfgPath, "add", dir, "--name", "envapp", "--web", "5173")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	if strings.Contains(out, "Which one") || strings.Contains(out, "No web route") {
		t.Errorf("--web answers the question:\n%s", out)
	}
	wantContains(t, out, "  5173  web     vite (VITE_PORT)\n")
}

// A lone port is the web route without a question.
func TestAddAsksNothingAboutALonePort(t *testing.T) {
	cfgPath, _, b := webAsking(t, true)
	out, err := executeWithInput(t, "", newAddCmd(b), "--config", cfgPath, "add", envProject(t, "PORT=3000\n"), "--name", "lone")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	if strings.Contains(out, "Which one") {
		t.Errorf("a lone port needs no question:\n%s", out)
	}
	wantContains(t, out, "  3000  web   port (PORT)\n")
}

// The preset offers and the web question read the same input, one line
// each, in the order they are asked.
func TestAddAsksAboutPresetsThenTheWebRoute(t *testing.T) {
	cfgPath, _, b := suggesting(t, true, "stack")
	out, err := executeWithInput(t, "n\n3\n", newAddCmd(b), "--config", cfgPath, "add", fixture(t, "sail-custom"), "--name", "myapp")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	wantContains(t, out, askStack, "Which one is the web app?\n", "  3) app (APP_PORT)", "  8080  web     app (APP_PORT)\n")
	if strings.Index(out, askStack) > strings.Index(out, "Which one") {
		t.Errorf("the preset is offered first:\n%s", out)
	}
}
