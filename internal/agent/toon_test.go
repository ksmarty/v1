package agent

import (
	"strings"
	"testing"
)

func TestToonWeatherForecast(t *testing.T) {
	in := `{"location":{"city":"Berlin","country":"DE","units":"metric"},"alerts":["frost","wind"],"forecast":[{"day":"Mon","temp":{"min":-2,"max":4},"condition":"snow","rainChance":80},{"day":"Tue","temp":{"min":1,"max":7},"condition":"cloudy","rainChance":20},{"day":"Wed","temp":{"min":3,"max":11},"condition":"sunny","rainChance":5}]}`
	want := `location:
  city: Berlin
  country: DE
  units: metric
alerts[2]: frost,wind
forecast[3]{day,temp{min,max},condition,rainChance}:
  Mon,-2,4,snow,80
  Tue,1,7,cloudy,20
  Wed,3,11,sunny,5`
	if got := toonJSON(in); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestToonKeyedTabular(t *testing.T) {
	in := `{"environments":{"production":{"region":"eu-central-1","replicas":6,"debug":false},"staging":{"region":"eu-central-1","replicas":2,"debug":true}}}`
	want := `environments[2:]{region,replicas,debug}:
  production: eu-central-1,6,false
  staging: eu-central-1,2,true`
	if got := toonJSON(in); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestToonRootKeyedTabular(t *testing.T) {
	in := `{"production":{"region":"eu-central-1","replicas":6},"staging":{"region":"eu-central-1","replicas":2}}`
	want := `[2:]{region,replicas}:
  production: eu-central-1,6
  staging: eu-central-1,2`
	if got := toonJSON(in); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestToonListFormNonUniform(t *testing.T) {
	in := `{"items":[{"name":"a","tags":["x","y"]},{"name":"b","tags":["z"]}]}`
	want := `items[2]:
  - name: a
    tags[2]: x,y
  - name: b
    tags[1]: z`
	if got := toonJSON(in); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestToonQuoting(t *testing.T) {
	in := `{"s1":"hello","s2":"42","s3":"true","s4":"","s5":"a,b","s6":"key: value","s7":"-dash","s8":"#tag","s9":"plain words","s10":"line\nbreak","s11":"trailing "}`
	got := toonJSON(in)
	for _, want := range []string{
		"s1: hello",
		`s2: "42"`,
		`s3: "true"`,
		`s4: ""`,
		`s5: "a,b"`,
		`s6: "key: value"`,
		`s7: "-dash"`,
		`s8: "#tag"`,
		`s9: plain words`,
		`s10: "line\nbreak"`,
		`s11: "trailing "`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
}

func TestToonOrderPreserved(t *testing.T) {
	in := `{"z":1,"a":2,"m":{"b":3,"c":4}}`
	want := "z: 1\na: 2\nm:\n  b: 3\n  c: 4"
	if got := toonJSON(in); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestToonNumbersAndNull(t *testing.T) {
	in := `{"big":12345678901234567890,"float":1.5,"neg":-3,"nothing":null,"on":true,"off":false}`
	want := "big: 12345678901234567890\nfloat: 1.5\nneg: -3\nnothing: null\non: true\noff: false"
	if got := toonJSON(in); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestToonEmptyShapes(t *testing.T) {
	if got := toonJSON(`{}`); got != "" {
		t.Fatalf("empty object = %q", got)
	}
	if got := toonJSON(`[]`); got != "[]" {
		t.Fatalf("empty array = %q", got)
	}
	if got := toonJSON(`{"a":{},"b":[]}`); got != "a:\nb: []" {
		t.Fatalf("empty nested = %q", got)
	}
}

func TestToonKeyedTabularRequiresUniform(t *testing.T) {
	// Values differ in shape — must fall back to nested objects, not keyed.
	in := `{"envs":{"production":{"replicas":6},"staging":{"replicas":2,"debug":true}}}`
	got := toonJSON(in)
	if strings.Contains(got, "[2:]") {
		t.Fatalf("should not use keyed tabular for non-uniform values:\n%s", got)
	}
	if !strings.Contains(got, "envs:") || !strings.Contains(got, "production:") {
		t.Fatalf("unexpected output:\n%s", got)
	}
}

func TestToonPassthroughNonJSON(t *testing.T) {
	if got := toonJSON("not json at all"); got != "not json at all" {
		t.Fatalf("non-JSON should pass through unchanged, got %q", got)
	}
	if got := toonJSON(""); got != "" {
		t.Fatalf("empty input should pass through, got %q", got)
	}
}

func TestToonMixedArrayListForm(t *testing.T) {
	in := `{"rows":[[1,2],[3]]}`
	want := `rows[2]:
  - [2]: 1,2
  - [1]: 3`
	if got := toonJSON(in); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestToonTabularDisqualifiedByNullMix(t *testing.T) {
	// A column mixing null (primitive) with objects disqualifies tabular form.
	in := `{"rows":[{"a":1,"b":{"x":1}},{"a":2,"b":null}]}`
	got := toonJSON(in)
	if strings.Contains(got, "{a,b{") {
		t.Fatalf("mixed null/object column must not use tabular form:\n%s", got)
	}
}
