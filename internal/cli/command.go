package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
)

const usage = `xxl-job: manage self-hosted XXL-JOB

Usage: xxl-job [global flags] <resource> <command> [flags]

Resources:
  config show       Show the resolved configuration without credentials
  group list        List executor groups
  job list|get      List jobs or retrieve a job by ID
  job create|update  Create a job or apply a JSON field patch
  job start|stop|trigger|delete  Change a job's state
  job next          Preview upcoming schedule times
  log list|get      List executions or retrieve one log segment

Global flags (must precede the resource):
  --config PATH      JSON configuration file
  --profile NAME     Profile in the configuration file
  --endpoint URL     Admin base URL including any context path
  --api-version VER  2, 3.0, 3.1, 3.2, 3.3, 3.4, or 3 (default 3)
  --allow-http       Permit a non-loopback HTTP endpoint

Run a command with --help for its flags.
`

func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if err := run(args, stdin, stdout); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		_ = json.NewEncoder(stderr).Encode(map[string]any{"error": map[string]string{"message": err.Error()}})
		return 1
	}
	return 0
}

func run(args []string, stdin io.Reader, out io.Writer) error {
	global := flags("xxl-job", out)
	var o options
	global.StringVar(&o.Config, "config", "", "configuration file")
	global.StringVar(&o.Profile, "profile", "", "configuration profile")
	global.StringVar(&o.Endpoint, "endpoint", "", "XXL-JOB Admin base URL")
	global.StringVar(&o.APIVersion, "api-version", "", "API version: 2, 3.0, 3.1, 3.2, 3.3, 3.4, or 3")
	global.BoolVar(&o.AllowHTTP, "allow-http", false, "allow non-loopback HTTP")
	global.Usage = func() { fmt.Fprint(out, usage) }
	if err := global.Parse(args); err != nil {
		return err
	}
	args = global.Args()
	if len(args) == 0 {
		fmt.Fprint(out, usage)
		return nil
	}
	if len(args) == 2 && (args[1] == "--help" || args[1] == "-h") {
		fmt.Fprint(out, usage)
		return nil
	}
	if len(args) < 2 {
		return errors.New("expected a resource and command; use --help")
	}
	switch args[0] + " " + args[1] {
	case "config show":
		if len(args) != 2 {
			return errors.New("config show takes no arguments")
		}
		s, err := loadSettings(o, false)
		if err != nil {
			return err
		}
		return output(out, map[string]any{"data": map[string]any{
			"profile": s.Profile, "endpoint": s.Endpoint.String(), "username": s.Username,
			"apiVersion": s.APIVersion, "allowHttp": s.AllowHTTP, "passwordConfigured": s.Password != "",
		}})
	case "group list":
		return listGroups(o, args[2:], out)
	case "job list":
		return listJobs(o, args[2:], out)
	case "job get":
		return getJob(o, args[2:], out)
	case "job create", "job update":
		return writeJob(o, args[1], args[2:], stdin, out)
	case "job start", "job stop", "job delete", "job trigger":
		return jobAction(o, args[1], args[2:], out)
	case "job next":
		return nextTimes(o, args[2:], out)
	case "log list":
		return listLogs(o, args[2:], out)
	case "log get":
		return getLog(o, args[2:], out)
	default:
		return fmt.Errorf("unknown command %q; use --help", strings.Join(args[:2], " "))
	}
}

func flags(name string, out io.Writer) *flag.FlagSet {
	f := flag.NewFlagSet(name, flag.ContinueOnError)
	f.SetOutput(io.Discard)
	f.Usage = func() {
		fmt.Fprintf(out, "Usage: xxl-job %s [flags]\n", name)
		f.SetOutput(out)
		f.PrintDefaults()
		f.SetOutput(io.Discard)
	}
	return f
}

func parseFlags(f *flag.FlagSet, args []string) error {
	if err := f.Parse(args); err != nil {
		return err
	}
	if len(f.Args()) > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(f.Args(), " "))
	}
	return nil
}

func output(out io.Writer, value any) error {
	enc := json.NewEncoder(out)
	enc.SetEscapeHTML(false)
	return enc.Encode(value)
}

func connect(o options) (*client, error) {
	s, err := loadSettings(o, true)
	if err != nil {
		return nil, err
	}
	c, err := newClient(s)
	if err != nil {
		return nil, err
	}
	if err := c.login(); err != nil {
		return nil, err
	}
	return c, nil
}

func pagination(f *flag.FlagSet) (*int, *int) {
	return f.Int("page", 1, "page number (starting at 1)"), f.Int("page-size", 20, "results per page (1-1000)")
}

func validatePage(number, size int) error {
	if number < 1 || size < 1 || size > 1000 || number > int(^uint(0)>>1)/size {
		return errors.New("page must be >= 1 and page-size must be between 1 and 1000")
	}
	return nil
}

func listGroups(o options, args []string, out io.Writer) error {
	f := flags("group list", out)
	number, size := pagination(f)
	appname := f.String("app-name", "", "filter by executor app name")
	title := f.String("title", "", "filter by executor group title")
	if err := parseFlags(f, args); err != nil {
		return err
	}
	if err := validatePage(*number, *size); err != nil {
		return err
	}
	c, err := connect(o)
	if err != nil {
		return err
	}
	p, err := c.list("/jobgroup/pageList", url.Values{"appname": {*appname}, "title": {*title}}, *number, *size)
	if err != nil {
		return err
	}
	return output(out, p)
}

func jobFilters(group, status int, desc, handler, author string) url.Values {
	return url.Values{"jobGroup": {strconv.Itoa(group)}, "triggerStatus": {strconv.Itoa(status)},
		"jobDesc": {desc}, "executorHandler": {handler}, "author": {author}}
}

func listJobs(o options, args []string, out io.Writer) error {
	f := flags("job list", out)
	number, size := pagination(f)
	group := f.Int("group", 0, "executor group ID (required)")
	status := f.Int("status", -1, "-1 all, 0 stopped, 1 running")
	desc := f.String("description", "", "filter by job description")
	handler := f.String("handler", "", "filter by handler")
	author := f.String("author", "", "filter by author")
	if err := parseFlags(f, args); err != nil {
		return err
	}
	if *group < 1 || (*status != -1 && *status != 0 && *status != 1) {
		return errors.New("a positive --group and a valid --status are required")
	}
	if err := validatePage(*number, *size); err != nil {
		return err
	}
	c, err := connect(o)
	if err != nil {
		return err
	}
	p, err := c.list("/jobinfo/pageList", jobFilters(*group, *status, *desc, *handler, *author), *number, *size)
	if err != nil {
		return err
	}
	return output(out, p)
}

func getJob(o options, args []string, out io.Writer) error {
	f := flags("job get", out)
	group := f.Int("group", 0, "executor group ID (required)")
	id := f.Int("id", 0, "job ID (required)")
	if err := parseFlags(f, args); err != nil {
		return err
	}
	if *group < 1 || *id < 1 {
		return errors.New("positive --group and --id are required")
	}
	c, err := connect(o)
	if err != nil {
		return err
	}
	job, err := c.findJob(*group, *id)
	if err != nil {
		return err
	}
	return output(out, map[string]any{"data": job})
}

func listLogs(o options, args []string, out io.Writer) error {
	f := flags("log list", out)
	number, size := pagination(f)
	group := f.Int("group", 0, "executor group ID (required)")
	jobID := f.Int("job-id", 0, "job ID (required)")
	status := f.Int("status", -1, "-1 all, 1 success, 2 failed, 3 running")
	timeRange := f.String("time-range", "", "YYYY-MM-DD HH:mm:ss - YYYY-MM-DD HH:mm:ss")
	if err := parseFlags(f, args); err != nil {
		return err
	}
	if *group < 1 || *jobID < 1 || (*status != -1 && *status != 1 && *status != 2 && *status != 3) {
		return errors.New("positive --group and --job-id and a valid --status are required")
	}
	if err := validatePage(*number, *size); err != nil {
		return err
	}
	c, err := connect(o)
	if err != nil {
		return err
	}
	p, err := c.list("/joblog/pageList", url.Values{"jobGroup": {strconv.Itoa(*group)}, "jobId": {strconv.Itoa(*jobID)},
		"logStatus": {strconv.Itoa(*status)}, "filterTime": {*timeRange}}, *number, *size)
	if err != nil {
		return err
	}
	return output(out, p)
}

func getLog(o options, args []string, out io.Writer) error {
	f := flags("log get", out)
	id := f.Int64("id", 0, "execution log ID (required)")
	line := f.Int("from-line", 1, "first log line")
	address := f.String("executor-address", "", "executor address (required for API 2)")
	trigger := f.Int64("trigger-time", 0, "trigger time in Unix milliseconds (required for API 2)")
	if err := parseFlags(f, args); err != nil {
		return err
	}
	if *id < 1 || *line < 1 {
		return errors.New("positive --id and --from-line are required")
	}
	c, err := connect(o)
	if err != nil {
		return err
	}
	form := url.Values{"logId": {strconv.FormatInt(*id, 10)}, "fromLineNum": {strconv.Itoa(*line)}}
	if c.settings.APIVersion == "2" {
		if *address == "" || *trigger < 1 {
			return errors.New("API 2 requires --executor-address and --trigger-time")
		}
		form.Set("executorAddress", *address)
		form.Set("triggerTime", strconv.FormatInt(*trigger, 10))
	}
	data, err := c.request("/joblog/logDetailCat", form)
	if err != nil {
		return err
	}
	return output(out, map[string]any{"data": data})
}

func nextTimes(o options, args []string, out io.Writer) error {
	f := flags("job next", out)
	kind := f.String("type", "CRON", "schedule type")
	schedule := f.String("schedule", "", "schedule expression (required)")
	if err := parseFlags(f, args); err != nil {
		return err
	}
	if *schedule == "" {
		return errors.New("--schedule is required")
	}
	c, err := connect(o)
	if err != nil {
		return err
	}
	data, err := c.request("/jobinfo/nextTriggerTime", url.Values{"scheduleType": {*kind}, "scheduleConf": {*schedule}})
	if err != nil {
		return err
	}
	return output(out, map[string]any{"data": data})
}

func jobAction(o options, action string, args []string, out io.Writer) error {
	f := flags("job "+action, out)
	id := f.Int("id", 0, "job ID (required)")
	dry := f.Bool("dry-run", false, "show the request without executing it")
	var param, address *string
	if action == "trigger" {
		param = f.String("param", "", "override execution parameter")
		address = f.String("address-list", "", "override executor addresses")
	}
	if err := parseFlags(f, args); err != nil {
		return err
	}
	if *id < 1 {
		return errors.New("a positive --id is required")
	}
	s, err := loadSettings(o, !*dry)
	if err != nil {
		return err
	}
	path := "/jobinfo/" + action
	form := url.Values{}
	if action == "trigger" {
		form.Set("id", strconv.Itoa(*id))
		form.Set("executorParam", *param)
		form.Set("addressList", *address)
	} else if !modernAPI(s.APIVersion) {
		if action == "delete" {
			path = "/jobinfo/remove"
		}
		form.Set("id", strconv.Itoa(*id))
	} else {
		form.Set("ids[]", strconv.Itoa(*id))
	}
	return executeWrite(s, path, form, *dry, out)
}

func executeWrite(s settings, path string, form url.Values, dry bool, out io.Writer) error {
	if dry {
		return output(out, map[string]any{"dryRun": true, "request": map[string]any{"method": "POST", "path": path, "form": form}})
	}
	c, err := newClient(s)
	if err != nil {
		return err
	}
	if err := c.login(); err != nil {
		return err
	}
	data, err := c.request(path, form)
	if err != nil {
		return err
	}
	return output(out, map[string]any{"data": data})
}

func readObject(path string, stdin io.Reader) (map[string]any, error) {
	var r io.Reader = stdin
	if path != "-" {
		file, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open input: %w", err)
		}
		defer file.Close()
		r = file
	}
	data, err := io.ReadAll(io.LimitReader(r, 1<<20+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 1<<20 {
		return nil, errors.New("input exceeds 1 MiB")
	}
	var obj map[string]any
	if err := decodeJSON(data, &obj); err != nil {
		return nil, err
	}
	if obj == nil {
		return nil, errors.New("input must be a JSON object")
	}
	return obj, nil
}
