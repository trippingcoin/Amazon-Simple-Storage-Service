package cluster

import "testing"

func TestSecureConfig(t *testing.T) {
	base := Config{DSN: "postgres://user@db.example/test?sslmode=verify-full", Bucket: "private-data", Region: "us-east-1"}
	if e := base.Validate(); e != nil {
		t.Fatal(e)
	}
	for _, change := range []func(*Config){func(c *Config) { c.DSN = "postgres://user@db.example/test?sslmode=disable" }, func(c *Config) { c.Endpoint = "http://storage.example" }, func(c *Config) { c.Endpoint = "https://user:secret@storage.example" }, func(c *Config) { c.Bucket = "../private" }, func(c *Config) { c.Region = "" }} {
		c := base
		change(&c)
		if c.Validate() == nil {
			t.Fatal("insecure config accepted")
		}
	}
	base.AllowInsecure = true
	base.DSN = "postgres://user@127.0.0.1/test?sslmode=disable"
	base.Endpoint = "http://127.0.0.1:9000"
	if e := base.Validate(); e != nil {
		t.Fatal(e)
	}
}
