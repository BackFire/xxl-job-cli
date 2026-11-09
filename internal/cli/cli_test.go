package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func cleanEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"XXL_JOB_CONFIG", "XXL_JOB_PROFILE", "XXL_JOB_ENDPOINT", "XXL_JOB_API_VERSION", "XXL_JOB_ALLOW_HTTP", "XXL_JOB_USERNAME", "XXL_JOB_PASSWORD"} {
		t.Setenv(key, "")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}

func invoke(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := Run(args, strings.NewReader(stdin), &out, &errOut)
	return code, out.String(), errOut.String()
}

func baseArgs(serverURL, version string, args ...string) []string {
	return append([]string{"--endpoint", serverURL + "/admin", "--api-version", version}, args...)
}

func jobRecord(id int) string {
	return fmt.Sprintf(`{"id":%d,"jobGroup":5,"jobDesc":"original","author":"owner","alarmEmail":null,"scheduleType":"CRON","scheduleConf":"0 0 * * * ?","misfireStrategy":"DO_NOTHING","executorRouteStrategy":"FIRST","executorHandler":"sync","executorParam":"secret=value","executorBlockStrategy":"SERIAL_EXECUTION","executorTimeout":0,"executorFailRetryCount":2,"childJobId":"","glueType":"BEAN"}`, id)
}

func TestVersionedReadAndWrite(t *testing.T) {
	for _, version := range []string{"2", "3.0", "3.1", "3.2", "3.3", "3.4", "3"} {
		t.Run("api-"+version, func(t *testing.T) {
			cleanEnv(t)
			t.Setenv("XXL_JOB_USERNAME", "alice")
			t.Setenv("XXL_JOB_PASSWORD", "test-password")
			var updates, actions int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					t.Errorf("method = %s", r.Method)
				}
				if err := r.ParseForm(); err != nil {
					t.Error(err)
				}
				loginPath := "/admin/auth/doLogin"
				if legacyLogin(version) {
					loginPath = "/admin/login"
				}
				if r.URL.Path == loginPath {
					if r.Form.Get("userName") != "alice" || r.Form.Get("password") != "test-password" {
						t.Error("incorrect login form")
					}
					http.SetCookie(w, &http.Cookie{Name: "SESSION", Value: "session", Path: "/admin/"})
					fmt.Fprint(w, `{"code":200,"msg":null}`)
					return
				}
				cookie, err := r.Cookie("SESSION")
				if err != nil || cookie.Value != "session" {
					t.Error("missing session cookie")
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				switch r.URL.Path {
				case "/admin/jobgroup/pageList", "/admin/jobinfo/pageList", "/admin/joblog/pageList":
					start, length := "offset", "pagesize"
					if !modernAPI(version) {
						start, length = "start", "length"
					}
					if r.Form.Get(start) == "" || r.Form.Get(length) == "" {
						t.Errorf("missing pagination: %v", r.Form)
					}
					data := `[]`
					if r.URL.Path == "/admin/jobinfo/pageList" {
						if r.Form.Get("jobGroup") != "5" || r.Form.Get("triggerStatus") != "-1" {
							t.Errorf("bad job filters: %v", r.Form)
						}
						data = "[" + jobRecord(41) + "]"
					}
					if modernAPI(version) {
						fmt.Fprintf(w, `{"code":200,"data":{"data":%s,"total":1}}`, data)
					} else {
						fmt.Fprintf(w, `{"data":%s,"recordsTotal":1,"recordsFiltered":1}`, data)
					}
				case "/admin/jobinfo/update":
					updates++
					if r.Form.Get("id") != "41" || r.Form.Get("jobGroup") != "5" || r.Form.Get("scheduleConf") != "0 30 * * * ?" || r.Form.Get("executorParam") != "secret=value" || r.Form.Get("executorFailRetryCount") != "2" {
						t.Errorf("update did not preserve fields: %v", r.Form)
					}
					if _, exists := r.Form["glueType"]; exists {
						t.Error("update must not claim to update glueType")
					}
					if _, exists := r.Form["alarmEmail"]; exists {
						t.Error("update must preserve a null alarmEmail")
					}
					fmt.Fprint(w, `{"code":200,"data":null}`)
				case "/admin/jobinfo/start":
					actions++
					key := "ids[]"
					if !modernAPI(version) {
						key = "id"
					}
					if r.Form.Get(key) != "41" {
						t.Errorf("bad action form: %v", r.Form)
					}
					fmt.Fprint(w, `{"code":200,"data":null}`)
				case "/admin/joblog/logDetailCat":
					if r.Form.Get("logId") != "99" || r.Form.Get("fromLineNum") != "3" {
						t.Errorf("bad log detail form: %v", r.Form)
					}
					if version == "2" && (r.Form.Get("executorAddress") != "http://executor" || r.Form.Get("triggerTime") != "12345") {
						t.Errorf("missing legacy log arguments: %v", r.Form)
					}
					if modernAPI(version) {
						fmt.Fprint(w, `{"code":200,"data":{"logContent":"ok"}}`)
					} else {
						fmt.Fprint(w, `{"code":200,"content":{"logContent":"ok"}}`)
					}
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			for _, command := range [][]string{{"group", "list"}, {"job", "list", "--group", "5"}, {"job", "get", "--group", "5", "--id", "41"}, {"log", "list", "--group", "5", "--job-id", "41"}} {
				code, stdout, stderr := invoke(t, "", baseArgs(server.URL, version, command...)...)
				if code != 0 || !json.Valid([]byte(stdout)) {
					t.Fatalf("%v: code=%d out=%s err=%s", command, code, stdout, stderr)
				}
			}
			logArgs := []string{"log", "get", "--id", "99", "--from-line", "3"}
			if version == "2" {
				logArgs = append(logArgs, "--executor-address", "http://executor", "--trigger-time", "12345")
			}
			if code, _, stderr := invoke(t, "", baseArgs(server.URL, version, logArgs...)...); code != 0 {
				t.Fatalf("log get: %s", stderr)
			}
			patch := `{"scheduleConf":"0 30 * * * ?"}`
			code, stdout, stderr := invoke(t, patch, baseArgs(server.URL, version, "job", "update", "--group", "5", "--id", "41", "--file", "-", "--dry-run")...)
			if code != 0 || !strings.Contains(stdout, `"dryRun":true`) || updates != 0 {
				t.Fatalf("update preview: code=%d out=%s err=%s updates=%d", code, stdout, stderr, updates)
			}
			if code, _, stderr := invoke(t, patch, baseArgs(server.URL, version, "job", "update", "--group", "5", "--id", "41", "--file", "-")...); code != 0 || updates != 1 {
				t.Fatalf("update: %s updates=%d", stderr, updates)
			}
			if code, _, stderr := invoke(t, "", baseArgs(server.URL, version, "job", "start", "--id", "41")...); code != 0 || actions != 1 {
				t.Fatalf("start: %s actions=%d", stderr, actions)
			}
		})
	}
}

func TestCreateAndActionDryRun(t *testing.T) {
	cleanEnv(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	job := `{"jobGroup":5,"jobDesc":"example","author":"owner","scheduleType":"CRON","scheduleConf":"0 0 * * * ?","misfireStrategy":"DO_NOTHING","executorRouteStrategy":"FIRST","executorHandler":"sync","executorBlockStrategy":"SERIAL_EXECUTION","glueType":"BEAN"}`
	for _, tc := range []struct{ version, action, wantPath, wantKey string }{
		{"3", "create", "/jobinfo/insert", "jobGroup"},
		{"2", "create", "/jobinfo/add", "jobGroup"},
		{"3", "delete", "/jobinfo/delete", "ids[]"},
		{"2", "delete", "/jobinfo/remove", "id"},
		{"3", "trigger", "/jobinfo/trigger", "executorParam"},
	} {
		args := []string{"job", tc.action, "--dry-run"}
		if tc.action == "create" {
			args = append(args, "--file", "-")
		} else {
			args = append(args, "--id", "41")
		}
		code, stdout, stderr := invoke(t, job, baseArgs(server.URL, tc.version, args...)...)
		if code != 0 || !strings.Contains(stdout, tc.wantPath) || !strings.Contains(stdout, tc.wantKey) {
			t.Fatalf("%+v: code=%d out=%s err=%s", tc, code, stdout, stderr)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("dry-run sent a request")
	}
}

func TestUpdateRejectsUnsafePatchAndIncompleteServerData(t *testing.T) {
	cleanEnv(t)
	t.Setenv("XXL_JOB_USERNAME", "alice")
	t.Setenv("XXL_JOB_PASSWORD", "test-password")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path == "/admin/auth/doLogin" {
			http.SetCookie(w, &http.Cookie{Name: "SESSION", Value: "session", Path: "/admin"})
			fmt.Fprint(w, `{"code":200}`)
			return
		}
		fmt.Fprint(w, `{"code":200,"data":{"total":1,"data":[{"id":41,"jobGroup":5,"jobDesc":"original"}]}}`)
	}))
	defer server.Close()
	args := baseArgs(server.URL, "3", "job", "update", "--group", "5", "--id", "41", "--file", "-")
	code, _, stderr := invoke(t, `{"jobGroup":6}`, args...)
	if code == 0 || !strings.Contains(stderr, "not supported") || calls.Load() != 0 {
		t.Fatalf("identity patch should be rejected locally: %s", stderr)
	}
	code, _, stderr = invoke(t, `{"jobDesc":"changed"}`, args...)
	if code == 0 || !strings.Contains(stderr, "refusing an incomplete update") || calls.Load() != 2 {
		t.Fatalf("incomplete response should not be written: %s, calls=%d", stderr, calls.Load())
	}
}

func TestErrorsAndNoWriteRetry(t *testing.T) {
	cleanEnv(t)
	t.Setenv("XXL_JOB_USERNAME", "alice")
	t.Setenv("XXL_JOB_PASSWORD", "do-not-print-this")
	var writes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/admin/auth/doLogin" {
			http.SetCookie(w, &http.Cookie{Name: "SESSION", Value: "session", Path: "/admin"})
			fmt.Fprint(w, `{"code":200}`)
			return
		}
		writes.Add(1)
		fmt.Fprint(w, `{"code":500,"msg":"refused"}`)
	}))
	defer server.Close()
	code, stdout, stderr := invoke(t, "", baseArgs(server.URL, "3", "job", "start", "--id", "41")...)
	if code == 0 || stdout != "" || !json.Valid([]byte(stderr)) || !strings.Contains(stderr, "refused") || writes.Load() != 1 {
		t.Fatalf("error handling: code=%d out=%s err=%s writes=%d", code, stdout, stderr, writes.Load())
	}
	if strings.Contains(stderr, "do-not-print-this") {
		t.Fatal("password leaked")
	}
}

func TestRedirectAndMalformedPageFail(t *testing.T) {
	for _, tc := range []struct {
		name     string
		body     string
		redirect bool
	}{
		{name: "redirect", redirect: true},
		{name: "html", body: "<html>login</html>"},
		{name: "missing-total", body: `{"code":200,"data":{"data":[]}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cleanEnv(t)
			t.Setenv("XXL_JOB_USERNAME", "alice")
			t.Setenv("XXL_JOB_PASSWORD", "secret")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/admin/auth/doLogin" {
					http.SetCookie(w, &http.Cookie{Name: "SESSION", Value: "session", Path: "/admin"})
					fmt.Fprint(w, `{"code":200}`)
					return
				}
				if tc.redirect {
					http.Redirect(w, r, "https://example.invalid/", http.StatusFound)
					return
				}
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			code, stdout, stderr := invoke(t, "", baseArgs(server.URL, "3", "job", "list", "--group", "5")...)
			if code == 0 || stdout != "" || !json.Valid([]byte(stderr)) {
				t.Fatalf("expected JSON error: code=%d out=%s err=%s", code, stdout, stderr)
			}
		})
	}
}

func TestProfileAndConfigShow(t *testing.T) {
	cleanEnv(t)
	t.Setenv("DEMO_PASSWORD", "do-not-print-this")
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	config := `{"defaultProfile":"demo","profiles":{"demo":{"endpoint":"http://127.0.0.1:8080/xxl-job-admin","username":"alice","passwordEnv":"DEMO_PASSWORD","apiVersion":"3"}}}`
	if err := os.WriteFile(path, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := invoke(t, "", "--config", path, "config", "show")
	if code != 0 || !strings.Contains(stdout, `"passwordConfigured":true`) || strings.Contains(stdout+stderr, "do-not-print-this") {
		t.Fatalf("config show: code=%d out=%s err=%s", code, stdout, stderr)
	}
	if _, err := loadSettings(options{Endpoint: "http://192.0.2.1:8080"}, false); err == nil {
		t.Fatal("non-loopback HTTP was accepted without opt-in")
	}
	if _, err := loadSettings(options{Endpoint: "https://user:password@example.com"}, false); err == nil {
		t.Fatal("endpoint userinfo was accepted")
	}
}

func TestGetScansMultiplePages(t *testing.T) {
	cleanEnv(t)
	t.Setenv("XXL_JOB_USERNAME", "alice")
	t.Setenv("XXL_JOB_PASSWORD", "secret")
	var offsets []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/admin/auth/doLogin" {
			http.SetCookie(w, &http.Cookie{Name: "SESSION", Value: "session", Path: "/admin"})
			fmt.Fprint(w, `{"code":200}`)
			return
		}
		_ = r.ParseForm()
		offsets = append(offsets, r.Form.Get("offset"))
		id := 1
		if r.Form.Get("offset") == "100" {
			id = 41
		}
		fmt.Fprintf(w, `{"code":200,"data":{"total":101,"data":[%s]}}`, jobRecord(id))
	}))
	defer server.Close()
	code, stdout, stderr := invoke(t, "", baseArgs(server.URL, "3", "job", "get", "--group", "5", "--id", "41")...)
	if code != 0 || !strings.Contains(stdout, `"id":41`) || strings.Join(offsets, ",") != "0,100" {
		t.Fatalf("pagination: out=%s err=%s offsets=%v", stdout, stderr, offsets)
	}
}

func TestInputValidation(t *testing.T) {
	cleanEnv(t)
	for _, input := range []string{`null`, `[]`, `{"jobGroup":"5"}`, `{"id":1}`, `{"jobGroup":1} {}`, `{"jobGroup":-1}`} {
		code, _, stderr := invoke(t, input, "--endpoint", "http://127.0.0.1:8080", "job", "create", "--file", "-", "--dry-run")
		if code == 0 || !json.Valid([]byte(stderr)) {
			t.Fatalf("invalid input %s accepted: %s", input, stderr)
		}
	}
	if _, err := readObject("-", io.LimitReader(strings.NewReader(strings.Repeat("x", 1<<20+1)), 1<<20+1)); err == nil {
		t.Fatal("oversized input accepted")
	}
}

func TestCreateAndLifecycleWrites(t *testing.T) {
	for _, version := range []string{"2", "3.0", "3.1", "3.2", "3.3", "3.4", "3"} {
		t.Run("api-"+version, func(t *testing.T) {
			cleanEnv(t)
			t.Setenv("XXL_JOB_USERNAME", "alice")
			t.Setenv("XXL_JOB_PASSWORD", "secret")
			var observed []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = r.ParseForm()
				if r.URL.Path == "/admin/auth/doLogin" || r.URL.Path == "/admin/login" {
					http.SetCookie(w, &http.Cookie{Name: "SESSION", Value: "cookie", Path: "/admin"})
					fmt.Fprint(w, `{"code":200}`)
					return
				}
				observed = append(observed, r.URL.Path)
				switch r.URL.Path {
				case "/admin/jobinfo/add", "/admin/jobinfo/insert":
					if r.Form.Get("jobDesc") != "sync & check" || r.Form.Get("jobGroup") != "5" {
						t.Errorf("create form: %v", r.Form)
					}
				case "/admin/jobinfo/trigger":
					if r.Form.Get("id") != "41" || r.Form.Get("executorParam") != "a&b=中文 +" || r.Form.Get("addressList") != "http://executor:9999" {
						t.Errorf("trigger form: %v", r.Form)
					}
				case "/admin/jobinfo/delete", "/admin/jobinfo/remove", "/admin/jobinfo/stop":
					key := "ids[]"
					if !modernAPI(version) {
						key = "id"
					}
					if r.Form.Get(key) != "41" {
						t.Errorf("action form: %v", r.Form)
					}
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
				}
				if !modernAPI(version) {
					fmt.Fprint(w, `{"code":200,"content":"41"}`)
				} else {
					fmt.Fprint(w, `{"code":200,"data":"41"}`)
				}
			}))
			defer server.Close()
			job := `{"jobGroup":5,"jobDesc":"sync & check","author":"owner","scheduleType":"CRON","scheduleConf":"0 0 * * * ?","misfireStrategy":"DO_NOTHING","executorRouteStrategy":"FIRST","executorHandler":"sync","executorBlockStrategy":"SERIAL_EXECUTION","glueType":"BEAN"}`
			commands := [][]string{
				{"job", "create", "--file", "-"},
				{"job", "trigger", "--id", "41", "--param", "a&b=中文 +", "--address-list", "http://executor:9999"},
				{"job", "stop", "--id", "41"}, {"job", "delete", "--id", "41"},
			}
			for _, command := range commands {
				code, stdout, stderr := invoke(t, job, baseArgs(server.URL, version, command...)...)
				if code != 0 || !strings.Contains(stdout, `"data":"41"`) {
					t.Fatalf("%v: code=%d out=%s err=%s", command, code, stdout, stderr)
				}
			}
			want := "/admin/jobinfo/insert,/admin/jobinfo/trigger,/admin/jobinfo/stop,/admin/jobinfo/delete"
			if !modernAPI(version) {
				want = "/admin/jobinfo/add,/admin/jobinfo/trigger,/admin/jobinfo/stop,/admin/jobinfo/remove"
			}
			if strings.Join(observed, ",") != want {
				t.Fatalf("unexpected write calls: %v", observed)
			}
		})
	}
}

func TestLoginFailureAndSecretRedaction(t *testing.T) {
	cleanEnv(t)
	t.Setenv("XXL_JOB_USERNAME", "alice")
	t.Setenv("XXL_JOB_PASSWORD", "test-password")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"code":500,"msg":"test-password should not be displayed"}`)
	}))
	defer server.Close()
	code, stdout, stderr := invoke(t, "", baseArgs(server.URL, "3", "job", "start", "--id", "41")...)
	if code == 0 || stdout != "" || calls.Load() != 1 || strings.Contains(stderr, "test-password") || !strings.Contains(stderr, "[REDACTED]") {
		t.Fatalf("login failure: code=%d out=%s err=%s calls=%d", code, stdout, stderr, calls.Load())
	}
}

func TestWriteDisconnectIsNotRetried(t *testing.T) {
	cleanEnv(t)
	t.Setenv("XXL_JOB_USERNAME", "alice")
	t.Setenv("XXL_JOB_PASSWORD", "secret")
	var writes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/admin/auth/doLogin" {
			http.SetCookie(w, &http.Cookie{Name: "SESSION", Value: "cookie", Path: "/admin"})
			fmt.Fprint(w, `{"code":200}`)
			return
		}
		writes.Add(1)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close()
	}))
	defer server.Close()
	code, _, _ := invoke(t, "", baseArgs(server.URL, "3", "job", "trigger", "--id", "41")...)
	if code == 0 || writes.Load() != 1 {
		t.Fatalf("trigger must not be retried after disconnect: code=%d writes=%d", code, writes.Load())
	}
}
