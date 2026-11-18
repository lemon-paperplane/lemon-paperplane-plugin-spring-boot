package springboot

import (
	"regexp"
	"strings"
	"testing"

	sdk "github.com/lemon-paperplane/lemon-paperplane-main/lemon-paperplane-main-plugin-sdk"
)

func TestYAMLAndProfiles(t *testing.T) {
	docs := loadConfigs([]string{"testdata/res"})
	if got := profilesOf(docs); strings.Join(got, ",") != "dev,docker,prod" {
		t.Errorf("profiles %v", got)
	}
	if got := defaultProfiles(docs); len(got) != 1 || got[0] != "dev" {
		t.Errorf("default %v", got)
	}
	base := merge(docs, nil)
	port, ctx := serverInfo(base)
	if port != 8081 || ctx != "/api" {
		t.Errorf("base server %d %q", port, ctx)
	}
	if v := base.get("management.endpoints.web.exposure.include"); v != "health,info" {
		t.Errorf("list %q", v)
	}
	if v := base.get("spring.datasource.url"); v != "jdbc:mysql://localhost:3306/demo?useSSL=false" {
		t.Errorf("placeholder %q", v)
	}
	if p, _ := serverInfo(merge(docs, []string{"docker"})); p != 9000 {
		t.Errorf("on-profile doc port %d", p)
	}
	prod := merge(docs, []string{"prod"})
	if p, _ := serverInfo(prod); p != 80 {
		t.Errorf("prod port %d", p)
	}
	// 宽松绑定：server-addr 与 serverAddr 视为同一个键
	if v := prod.get("spring.cloud.nacos.serverAddr"); v != "nacos.prod:8848" {
		t.Errorf("relaxed %q", v)
	}
}

func TestServices(t *testing.T) {
	docs := loadConfigs([]string{"testdata/res"})
	dev := merge(docs, []string{"dev"})
	ss := detectServices(dev, []string{"org.springframework.boot:spring-boot-starter-data-redis", "org.springframework.boot:spring-boot-starter-amqp"})
	byKind := map[string]Service{}
	for _, s := range ss {
		byKind[s.Kind] = s
	}
	if byKind["mysql"].Address != "jdbc:mysql://10.0.0.5:3306/demo_dev" || byKind["mysql"].Source != "application-dev.yml" {
		t.Errorf("mysql %+v", byKind["mysql"])
	}
	if byKind["redis"].Address != "redis.dev.local:6380" {
		t.Errorf("redis %+v", byKind["redis"])
	}
	if byKind["rabbitmq"].Address != "mq.dev.local:5672" {
		t.Errorf("rabbit %+v", byKind["rabbitmq"])
	}
	if byKind["nacos"].Address != "127.0.0.1:8848" {
		t.Errorf("nacos from bootstrap %+v", byKind["nacos"])
	}
	if k, h := jdbcService("jdbc:postgresql://pg.prod:5432/app"); k != "postgresql" || h != "pg.prod:5432" {
		t.Errorf("jdbc %s %s", k, h)
	}
}

func TestURLAndReadyPatterns(t *testing.T) {
	m, err := sdk.LoadManifest("..")
	if err != nil {
		t.Fatal(err)
	}
	expand := func(line string) []string {
		var out []string
		for _, u := range m.Contributes.URLPatterns {
			re := regexp.MustCompile(u.Pattern)
			if sm := re.FindStringSubmatch(line); sm != nil {
				s := u.URL
				for i := 1; i < len(sm); i++ {
					s = strings.ReplaceAll(s, "{"+string(rune('0'+i))+"}", sm[i])
				}
				out = append(out, s)
			}
		}
		return out
	}
	cases := map[string]string{
		"o.s.b.w.embedded.tomcat.TomcatWebServer  : Tomcat started on port 18081 (http) with context path '/api'": "http://localhost:18081/api",
		"Tomcat started on port(s): 8080 (http) with context path ''":                                             "http://localhost:8080",
		"o.s.b.web.embedded.netty.NettyWebServer : Netty started on port 18070 (http)":                            "http://localhost:18070",
		"Netty started on port(s): 8080":                                                                          "http://localhost:8080",
		"Jetty started on port 8080 (http/1.1) with context path '/'":                                             "http://localhost:8080/",
		"Undertow started on port(s) 8080 (http) with context path ''":                                            "http://localhost:8080",
		"Tomcat started on port 8443 (https) with context path '/app'":                                            "https://localhost:8443/app",
	}
	for line, want := range cases {
		got := expand(line)
		if len(got) != 1 || got[0] != want {
			t.Errorf("%q → %v, want %s", line, got, want)
		}
	}
	ready := regexp.MustCompile(m.Contributes.ReadyPatterns[0].Pattern)
	for _, l := range []string{"Started DemoApplication in 0.872 seconds (process running for 1.003)", "Started App in 12 seconds (JVM running for 13.2)"} {
		if !ready.MatchString(l) {
			t.Errorf("ready %q", l)
		}
	}
	redis := regexp.MustCompile(m.Contributes.Diagnostics[0].Pattern)
	if sm := redis.FindStringSubmatch("io.lettuce.core.RedisConnectionException: Unable to connect to localhost/<unresolved>:6379"); sm == nil || sm[1] != "localhost" || sm[2] != "6379" {
		t.Errorf("redis rule %v", sm)
	}
}

func TestParseFailure(t *testing.T) {
	log := strings.Split(`2026-11-18T10:00:00 INFO main : Starting
***************************
APPLICATION FAILED TO START
***************************

Description:

Web server failed to start. Port 8080 was already in use.

Action:

Identify and stop the process that's listening on port 8080 or configure this application to listen on another port.

2026-11-18T10:00:01 INFO main : done`, "\n")
	fr := parseFailure(log)
	if fr == nil || !strings.Contains(fr.Description, "Port 8080 was already in use") || !strings.HasPrefix(fr.Action, "Identify and stop") || strings.Contains(fr.Action, "done") {
		t.Fatalf("%+v", fr)
	}
	if s := firstSentence(fr.Description); s != "Web server failed to start." {
		t.Errorf("title %q", s)
	}
}

func TestBootMainClass(t *testing.T) {
	bm := &buildModel{Projects: []buildProject{{Path: ":", Dir: "testdata", SourceDirs: []string{"testdata/src/main/java", "testdata/src/main/kotlin"}}}}
	apps := scanApps(bm)
	got := map[string]bool{}
	for _, a := range apps {
		got[a.MainClass] = true
	}
	if !got["com.acme.AcmeApp"] || !got["com.acme.Demo_appKt"] || len(apps) != 2 {
		t.Errorf("apps %+v", apps)
	}
	if kotlinFacade("main") != "MainKt" || kotlinFacade("my-file") != "My_fileKt" {
		t.Error("facade")
	}
	k := bootMainClass([]byte("@file:JvmName(\"Boot\")\npackage a\n@SpringBootApplication class X\nfun main() {}"), "x.kt", true)
	if k != "a.Boot" {
		t.Errorf("jvmname %q", k)
	}
	if bootMainClass([]byte("@SpringBootApplication public class NoMain {}"), "NoMain.java", false) != "" {
		t.Error("class without main should be skipped")
	}
}

func TestJdkConstraintAndIdea(t *testing.T) {
	for v, want := range map[string]string{"3.5.5": ">=17", "4.0.0": ">=17", "2.7.2": ">=8 <22", "2.3.12.RELEASE": ">=8 <16", "1.5.22.RELEASE": ">=8 <12", "": ""} {
		if got := jdkConstraint(v); got != want {
			t.Errorf("%s → %q want %q", v, got, want)
		}
	}
	bm := &buildModel{Tool: "gradle", RootName: "vrtalk-api", Projects: []buildProject{{Path: ":vrtalk-api-starter", Name: "vrtalk-api-starter"}}}
	if m := ideaModule("vrtalk-api.vrtalk-api-starter.main", bm); m != ":vrtalk-api-starter" {
		t.Errorf("idea module %q", m)
	}
}

func TestPanelUsesRunInfo(t *testing.T) {
	model := &Model{Apps: []App{{Name: "AcmeApp", MainClass: "com.acme.AcmeApp", ResourceDirs: []string{"testdata/res"}, Actuator: true, DefaultProfiles: []string{"dev"}, Port: 8081, URL: "http://localhost:8081/api"}}}
	run := &sdk.RunInfo{State: "running", Config: &sdk.RunConfiguration{Options: map[string]any{"mainClass": "com.acme.AcmeApp", "activeProfiles": []any{"prod"}}}, URLs: []string{"http://localhost:8099/api/"}}
	ps := panelsFor(model, run)
	if len(ps) != 1 {
		t.Fatalf("panels %d", len(ps))
	}
	p := ps[0]
	if strings.Join(p.profiles, ",") != "prod" || p.url != "http://localhost:8099/api" || p.port != 8099 || p.actuator != "http://localhost:8099/api/actuator" || !p.running {
		t.Errorf("panel %+v", p)
	}
	kinds := map[string]bool{}
	for _, s := range p.services {
		kinds[s.Kind] = true
	}
	if !kinds["postgresql"] || !kinds["nacos"] {
		t.Errorf("prod services %+v", p.services)
	}
	// 运行配置里写了端口、内核还没识别到 URL：用配置端口
	run = &sdk.RunInfo{State: "starting", Config: &sdk.RunConfiguration{Options: map[string]any{"mainClass": "com.acme.AcmeApp", "port": float64(19000)}}}
	if p := panelsFor(model, run)[0]; p.port != 19000 || p.url != "http://localhost:19000/api" || strings.Join(p.profiles, ",") != "dev" {
		t.Errorf("configured port %+v", p)
	}
	if len(panelsFor(model, nil)) != 1 {
		t.Error("fallback without run")
	}
}
