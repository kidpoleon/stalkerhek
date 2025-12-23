package stalker

import (
	"errors"
	"io/ioutil"
	"log"
	"math"
	"math/rand"
	"net/http"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v2"
)

// Config contains configuration taken from the YAML file.
type Config struct {
	Portal *Portal `yaml:"portal"`
	HLS    struct {
		Enabled bool   `yaml:"enabled"`
		Bind    string `yaml:"bind"`
	} `yaml:"hls"`
	Proxy struct {
		Enabled bool   `yaml:"enabled"`
		Bind    string `yaml:"bind"`
		Rewrite bool   `yaml:"rewrite"`
	} `yaml:"proxy"`
}

// HTTPClient with connection pooling
var HTTPClient = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	},
}

// Portal represents Stalker portal
type Portal struct {
	Model        string `yaml:"model"`
	SerialNumber string `yaml:"serial_number"`
	DeviceID     string `yaml:"device_id"`
	DeviceID2    string `yaml:"device_id2"`
	Signature    string `yaml:"signature"`
	MAC          string `yaml:"mac"`
	Username     string `yaml:"username"`
	Password     string `yaml:"password"`
	Location     string `yaml:"url"`
	TimeZone     string `yaml:"time_zone"`
	Token        string `yaml:"token"`
	WatchDogTime int    `yaml:"watchdog"`
	DeviceIdAuth bool   `yaml:"device_id_auth"`
}

// ReadConfig returns configuration from the file in Portal object
func ReadConfig(path *string) (*Config, error) {
	content, err := ioutil.ReadFile(*path)
	if err != nil {
		return nil, err
	}

	var c *Config
	err = yaml.Unmarshal(content, &c)
	if err != nil {
		return nil, err
	}

	if err = c.validateWithDefaults(); err != nil {
		return nil, err
	}
	return c, nil
}

var regexMAC = regexp.MustCompile(`^[A-F0-9]{2}:[A-F0-9]{2}:[A-F0-9]{2}:[A-F0-9]{2}:[A-F0-9]{2}:[A-F0-9]{2}$`)
var regexTimezone = regexp.MustCompile(`^[a-zA-Z]+/[a-zA-Z]+$`)

func (c *Config) validateWithDefaults() error {
	c.Portal.MAC = strings.ToUpper(c.Portal.MAC)

	if c.Portal.Model == "" {
		return errors.New("empty model")
	}

	if c.Portal.SerialNumber == "" {
		return errors.New("empty serial number (sn)")
	}

	if c.Portal.DeviceID == "" {
		return errors.New("empty device_id")
	}

	if c.Portal.DeviceID2 == "" {
		return errors.New("empty device_id2")
	}

	// Signature can be empty and it's fine

	if !regexMAC.MatchString(c.Portal.MAC) {
		return errors.New("invalid MAC '" + c.Portal.MAC + "'")
	}

	/* Username and password fields are optional */

	if c.Portal.Location == "" {
		return errors.New("empty portal url")
	}

	if !regexTimezone.MatchString(c.Portal.TimeZone) {
		return errors.New("invalid timezone '" + c.Portal.TimeZone + "'")
	}

	if !c.HLS.Enabled && !c.Proxy.Enabled {
		return errors.New("no services enabled")
	}

	if c.HLS.Enabled && c.HLS.Bind == "" {
		return errors.New("empty HLS bind")
	}

	if c.Proxy.Enabled && c.Proxy.Bind == "" {
		return errors.New("empty proxy bind")
	}

	if c.Proxy.Rewrite && !c.HLS.Enabled {
		return errors.New("HLS service must be enabled for 'proxy: rewrite'")
	}

	if c.Portal.Token == "" {
		c.Portal.Token = randomToken()
		log.Println("No token given, using random one:", c.Portal.Token)
	}

	if c.Portal.WatchDogTime == 1 {
		c.Portal.WatchDogTime = 2
		log.Println("Using Watchdog update interval = ", c.Portal.WatchDogTime)
	}

	return nil
}

// RetryConfig for retry logic
type RetryConfig struct {
	MaxRetries int
	BaseDelay  time.Duration
	MaxDelay   time.Duration
}

// RetryWithBackoff executes a function with exponential backoff retry
func RetryWithBackoff(config RetryConfig, fn func() error) error {
	var lastErr error
	for attempt := 0; attempt < config.MaxRetries; attempt++ {
		if err := fn(); err == nil {
			return nil
		} else {
			lastErr = err
			if attempt < config.MaxRetries-1 {
				delay := time.Duration(float64(config.BaseDelay) * math.Pow(2, float64(attempt)))
				if delay > config.MaxDelay {
					delay = config.MaxDelay
				}
				log.Printf("Attempt %d failed, retrying in %v: %v", attempt+1, delay, err)
				time.Sleep(delay)
			}
		}
	}
	return lastErr
}

func randomToken() string {
	allowlist := []rune("ABCDEF0123456789")
	b := make([]rune, 32)
	for i := range b {
		b[i] = allowlist[rand.Intn(len(allowlist))]
	}
	return string(b)
}
