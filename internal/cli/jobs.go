package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
)

var updateFields = map[string]bool{
	"jobDesc": false, "author": false, "alarmEmail": false,
	"scheduleType": false, "scheduleConf": false, "misfireStrategy": false,
	"executorRouteStrategy": false, "executorHandler": false, "executorParam": false,
	"executorBlockStrategy": false, "executorTimeout": true, "executorFailRetryCount": true,
	"childJobId": false,
}

var createExtraFields = map[string]bool{
	"jobGroup": true, "glueType": false, "glueSource": false, "glueRemark": false,
}

var requiredUpdateFields = map[string]bool{
	"jobDesc": true, "author": true, "scheduleType": true, "scheduleConf": true,
	"misfireStrategy": true, "executorRouteStrategy": true, "executorHandler": true,
	"executorBlockStrategy": true,
}

func writeJob(o options, action string, args []string, stdin io.Reader, out io.Writer) error {
	f := flags("job "+action, out)
	file := f.String("file", "", "JSON file or - for stdin (required)")
	dry := f.Bool("dry-run", false, "show the request without executing it")
	var group, id *int
	if action == "update" {
		group = f.Int("group", 0, "current executor group ID (required)")
		id = f.Int("id", 0, "job ID (required)")
	}
	if err := parseFlags(f, args); err != nil {
		return err
	}
	if *file == "" {
		return errors.New("--file is required")
	}
	if action == "update" && (*group < 1 || *id < 1) {
		return errors.New("positive --group and --id are required")
	}
	fields, err := readObject(*file, stdin)
	if err != nil {
		return err
	}
	if len(fields) == 0 {
		return errors.New("input object must not be empty")
	}
	if err := validateFields(fields, action == "create"); err != nil {
		return err
	}
	if action == "create" {
		return createJob(o, fields, *dry, out)
	}
	return updateJob(o, *group, *id, fields, *dry, out)
}

func validateFields(fields map[string]any, create bool) error {
	for name, value := range fields {
		numeric, ok := updateFields[name]
		if !ok && create {
			numeric, ok = createExtraFields[name]
		}
		if !ok {
			return fmt.Errorf("field %q is not supported for this operation", name)
		}
		if _, err := fieldValue(name, value, numeric); err != nil {
			return err
		}
	}
	return nil
}

func fieldValue(name string, value any, numeric bool) (string, error) {
	if numeric {
		n, ok := value.(json.Number)
		if !ok {
			return "", fmt.Errorf("field %q must be a non-negative integer", name)
		}
		i, err := strconv.ParseInt(n.String(), 10, 32)
		if err != nil || i < 0 {
			return "", fmt.Errorf("field %q must be a non-negative 32-bit integer", name)
		}
		return strconv.FormatInt(i, 10), nil
	}
	if value == nil {
		return "", nil
	}
	s, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("field %q must be a string or null", name)
	}
	return s, nil
}

func createJob(o options, fields map[string]any, dry bool, out io.Writer) error {
	required := []string{"jobGroup", "jobDesc", "author", "scheduleType", "scheduleConf", "misfireStrategy",
		"executorRouteStrategy", "executorHandler", "executorBlockStrategy", "glueType"}
	for _, name := range required {
		value, ok := fields[name]
		if !ok || value == nil {
			return fmt.Errorf("create requires field %q", name)
		}
		if name != "scheduleConf" && name != "executorHandler" && value == "" {
			return fmt.Errorf("create requires a non-empty field %q", name)
		}
	}
	if n, _ := fields["jobGroup"].(json.Number); n.String() == "0" {
		return errors.New("jobGroup must be positive")
	}
	form := make(url.Values, len(fields))
	for name, value := range fields {
		if value == nil {
			continue
		}
		numeric, ok := updateFields[name]
		if !ok {
			numeric = createExtraFields[name]
		}
		v, err := fieldValue(name, value, numeric)
		if err != nil {
			return err
		}
		form.Set(name, v)
	}
	s, err := loadSettings(o, !dry)
	if err != nil {
		return err
	}
	path := "/jobinfo/insert"
	if !modernAPI(s.APIVersion) {
		path = "/jobinfo/add"
	}
	return executeWrite(s, path, form, dry, out)
}

func updateJob(o options, group, id int, patch map[string]any, dry bool, out io.Writer) error {
	c, err := connect(o)
	if err != nil {
		return err
	}
	job, err := c.findJob(group, id)
	if err != nil {
		return err
	}
	jobGroup, err := fieldValue("jobGroup", job["jobGroup"], true)
	if err != nil || jobGroup != strconv.Itoa(group) {
		return errors.New("server returned an unexpected job group")
	}
	form := url.Values{"id": {strconv.Itoa(id)}, "jobGroup": {jobGroup}}
	for name, numeric := range updateFields {
		value, exists := job[name]
		if !exists {
			return fmt.Errorf("server response lacks field %q; refusing an incomplete update", name)
		}
		if replacement, ok := patch[name]; ok {
			value = replacement
		}
		if value == nil && !numeric {
			if requiredUpdateFields[name] {
				return fmt.Errorf("required field %q is null", name)
			}
			continue
		}
		v, err := fieldValue(name, value, numeric)
		if err != nil {
			return err
		}
		form.Set(name, v)
	}
	if strings.TrimSpace(form.Get("jobDesc")) == "" || strings.TrimSpace(form.Get("author")) == "" {
		return errors.New("jobDesc and author must not be empty")
	}
	if dry {
		return output(out, map[string]any{"dryRun": true, "request": map[string]any{"method": "POST", "path": "/jobinfo/update", "form": form}})
	}
	data, err := c.request("/jobinfo/update", form)
	if err != nil {
		return err
	}
	return output(out, map[string]any{"data": data})
}
