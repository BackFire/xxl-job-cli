package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type profile struct {
	Endpoint    string `json:"endpoint"`
	Username    string `json:"username"`
	PasswordEnv string `json:"passwordEnv"`
	APIVersion  string `json:"apiVersion"`
	AllowHTTP   bool   `json:"allowHttp"`
}

type configFile struct {
	DefaultProfile string             `json:"defaultProfile"`
	Profiles       map[string]profile `json:"profiles"`
}

type options struct {
	Config     string
	Profile    string
	Endpoint   string
	APIVersion string
	AllowHTTP  bool
}

type settings struct {
	Endpoint   *url.URL
	Username   string
	Password   string
	APIVersion string
	AllowHTTP  bool
	Profile    string
}

func defaultConfigPath() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "xxl-job", "config.json"), nil
}

func loadSettings(o options, requireCredentials bool) (settings, error) {
	configPath := o.Config
	if configPath == "" {
		configPath = os.Getenv("XXL_JOB_CONFIG")
	}
	explicitConfig := configPath != ""
	if configPath == "" {
		var err error
		configPath, err = defaultConfigPath()
		if err != nil {
			return settings{}, fmt.Errorf("resolve config path: %w", err)
		}
	}
	var file configFile
	contents, err := os.ReadFile(configPath)
	if err != nil && (!errors.Is(err, os.ErrNotExist) || explicitConfig) {
		return settings{}, fmt.Errorf("read config: %w", err)
	}
	if err == nil {
		decoder := json.NewDecoder(strings.NewReader(string(contents)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&file); err != nil {
			return settings{}, fmt.Errorf("parse config: %w", err)
		}
		if err := ensureEOF(decoder); err != nil {
			return settings{}, fmt.Errorf("parse config: %w", err)
		}
	}
	name := first(o.Profile, os.Getenv("XXL_JOB_PROFILE"), file.DefaultProfile)
	var p profile
	if name != "" {
		var ok bool
		p, ok = file.Profiles[name]
		if !ok {
			return settings{}, fmt.Errorf("profile %q not found", name)
		}
	}
	endpoint := first(o.Endpoint, os.Getenv("XXL_JOB_ENDPOINT"), p.Endpoint)
	apiVersion := first(o.APIVersion, os.Getenv("XXL_JOB_API_VERSION"), p.APIVersion, "3")
	switch apiVersion {
	case "2", "3.0", "3.1", "3.2", "3", "3.3", "3.4":
	default:
		return settings{}, errors.New("api version must be 2, 3.0, 3.1, 3.2, 3.3, 3.4, or 3")
	}
	allowHTTP := p.AllowHTTP || o.AllowHTTP
	if v := os.Getenv("XXL_JOB_ALLOW_HTTP"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return settings{}, fmt.Errorf("invalid XXL_JOB_ALLOW_HTTP: %w", err)
		}
		allowHTTP = b || o.AllowHTTP
	}
	u, err := url.Parse(endpoint)
	if err != nil || u == nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return settings{}, errors.New("endpoint must be an HTTP(S) URL without userinfo, query, or fragment")
	}
	if u.Scheme == "http" && !allowHTTP && !isLoopback(u.Hostname()) {
		return settings{}, errors.New("HTTP endpoint requires --allow-http or allowHttp in the profile")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	username := first(os.Getenv("XXL_JOB_USERNAME"), p.Username)
	passwordEnv := first(p.PasswordEnv, "XXL_JOB_PASSWORD")
	password := first(os.Getenv("XXL_JOB_PASSWORD"), os.Getenv(passwordEnv))
	if requireCredentials && (username == "" || password == "") {
		return settings{}, fmt.Errorf("username and password required; set XXL_JOB_USERNAME and %s", passwordEnv)
	}
	return settings{Endpoint: u, Username: username, Password: password, APIVersion: apiVersion, AllowHTTP: allowHTTP, Profile: name}, nil
}

func modernAPI(version string) bool {
	return version == "3" || version == "3.3" || version == "3.4"
}

func legacyLogin(version string) bool {
	return version == "2" || version == "3.0" || version == "3.1"
}

func first(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
