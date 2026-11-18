// Package springboot 是 Lemon Paperplane 的 Spring Boot 插件：识别 @SpringBootApplication、profile 与端口、
// 用构建插件编译后直接 java 启动（或 bootRun / spring-boot:run），解析 FailureAnalyzer 报错与中间件连接失败。
package springboot

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	sdk "github.com/lemon-paperplane/lemon-paperplane-main/lemon-paperplane-main-plugin-sdk"
)

const typeBoot = "spring-boot"

// Plugin 实现 spring-boot 插件。
type Plugin struct{}

// New 返回插件实例。
func New() any { return &Plugin{} }

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// Model 是 spring-boot 插件的模型。
type Model struct {
	BootVersion string   `json:"bootVersion,omitempty"`
	Apps        []App    `json:"apps"`
	Profiles    []string `json:"profiles,omitempty"`
}

// ---------------------------------------------------------------------------
// 识别

// Detect 在 Gradle / Maven 工程的构建文件里找 Spring Boot 插件或依赖。
func (p *Plugin) Detect(ctx *sdk.Context, d *sdk.DetectParams) (*sdk.DetectResult, error) {
	var dirs []string
	var files []string
	for _, f := range d.Facets {
		if f.ID != "gradle" && f.ID != "maven" {
			continue
		}
		var data struct {
			Projects []struct {
				Dir     string `json:"dir"`
				Android bool   `json:"android"`
			} `json:"projects"`
		}
		if b, err := json.Marshal(f.Data); err == nil {
			_ = json.Unmarshal(b, &data)
		}
		dirs = append(dirs, d.Dir)
		for _, pr := range data.Projects {
			if !pr.Android {
				dirs = append(dirs, filepath.Join(d.Dir, filepath.FromSlash(pr.Dir)))
			}
		}
		if f.ID == "gradle" {
			files = []string{"build.gradle", "build.gradle.kts"}
			dirs = append(dirs, filepath.Join(d.Dir, "gradle"))
			files = append(files, "libs.versions.toml")
		} else {
			files = []string{"pom.xml"}
		}
	}
	for _, dir := range dirs {
		for _, f := range files {
			text := sdk.ReadText(filepath.Join(dir, f), 512*1024)
			if strings.Contains(text, "org.springframework.boot") || strings.Contains(text, "spring-boot-starter") {
				return &sdk.DetectResult{Facets: []sdk.Facet{{ID: "spring-boot"}}}, nil
			}
		}
	}
	return &sdk.DetectResult{}, nil
}

// ---------------------------------------------------------------------------
// 分析

// Analyze 找出所有 Spring Boot 应用，读 profile、端口与中间件地址，生成运行配置。
func (p *Plugin) Analyze(ctx *sdk.Context, a *sdk.AnalyzeParams) (*sdk.AnalyzeResult, error) {
	bm := readBuildModel(&a.Module)
	apps := scanApps(bm)
	boot := bootVersionOf(bm)
	model := Model{BootVersion: boot}
	profileSet := map[string]bool{}
	names := map[string]int{}
	for i := range apps {
		app := &apps[i]
		if app.BootVersion == "" {
			app.BootVersion = boot
		}
		fillApp(app)
		for _, pf := range app.Profiles {
			profileSet[pf] = true
		}
		app.Name = simpleName(app.MainClass)
		if app.Kotlin && strings.HasSuffix(app.Name, "Kt") && len(app.Name) > 2 {
			// Kotlin 顶层 main 的门面类 DemoApplicationKt 显示为 DemoApplication
			app.Name = strings.TrimSuffix(app.Name, "Kt")
		}
		names[app.Name]++
	}
	for i := range apps {
		if names[apps[i].Name] > 1 && apps[i].Module != "" {
			apps[i].Name += " (" + strings.TrimPrefix(apps[i].Module, ":") + ")"
		}
	}
	model.Apps = apps
	for pf := range profileSet {
		model.Profiles = append(model.Profiles, pf)
	}
	sortStrings(model.Profiles)

	res := &sdk.AnalyzeResult{Model: mustJSON(model)}
	if boot != "" {
		res.Info = []sdk.InfoItem{{Label: "Spring Boot", Value: boot}}
		if c := jdkConstraint(boot); c != "" {
			res.Requirements = []sdk.ToolchainRequirement{{Kind: "jdk", Constraint: c, Source: "Spring Boot " + boot, Purpose: "runtime"}}
		}
	}
	opts := []sdk.Option{} // 没有选项也输出 []，不能是 null（界面按数组解析）
	for _, pf := range model.Profiles {
		opts = append(opts, sdk.Option{Value: pf})
	}
	res.OptionValues = map[string][]sdk.Option{"spring.profiles": opts}

	var configs []sdk.RunConfiguration
	for i, app := range apps {
		o := map[string]any{"mainClass": app.MainClass}
		if app.Module != "" && app.Module != ":" {
			o["module"] = app.Module
		}
		if len(app.DefaultProfiles) > 0 {
			o["activeProfiles"] = app.DefaultProfiles
		}
		configs = append(configs, sdk.RunConfiguration{
			ID: sdk.StableID(typeBoot, app.Module, app.MainClass), Name: app.Name, Type: typeBoot,
			Options: o, Source: "auto", Priority: 100, Default: i == 0,
		})
	}
	// IDEA 的 Spring Boot 运行配置
	for _, c := range a.IdeConfigs {
		if c.Type != "SpringBootApplicationConfigurationType" {
			continue
		}
		main := c.Options["SPRING_BOOT_MAIN_CLASS"]
		if main == "" {
			continue
		}
		o := map[string]any{"mainClass": main}
		if mod := ideaModule(c.Module, bm); mod != "" {
			o["module"] = mod
		}
		if pf := splitList(c.Options["ACTIVE_PROFILES"]); len(pf) > 0 {
			o["activeProfiles"] = pf
		} else {
			for _, app := range apps {
				if app.MainClass == main && len(app.DefaultProfiles) > 0 {
					o["activeProfiles"] = app.DefaultProfiles
				}
			}
		}
		if v := c.Options["VM_PARAMETERS"]; v != "" {
			o["vmOptions"] = v
		}
		if v := c.Options["PROGRAM_PARAMETERS"]; v != "" {
			o["programArgs"] = v
		}
		if v := c.Options["WORKING_DIRECTORY"]; v != "" && !strings.Contains(v, "$MODULE") {
			o["workingDir"] = strings.ReplaceAll(v, "$PROJECT_DIR$", a.Project.Root)
		}
		kept := configs[:0]
		wasDefault := false
		for _, x := range configs {
			if x.Options["mainClass"] == main {
				wasDefault = wasDefault || x.Default
				continue
			}
			kept = append(kept, x)
		}
		configs = append(kept, sdk.RunConfiguration{
			ID: sdk.StableID("idea", typeBoot, c.Name, main), Name: c.Name, Type: typeBoot, Options: o, Env: c.Env,
			Source: "idea", Priority: 110, Default: wasDefault || len(kept) == 0,
		})
	}
	res.RunConfigurations = configs
	return res, nil
}

// fillApp 读取应用的 profile、端口、上下文路径与中间件。
func fillApp(app *App) {
	docs := loadConfigs(app.ResourceDirs)
	app.Profiles = profilesOf(docs)
	app.DefaultProfiles = defaultProfiles(docs)
	e := merge(docs, app.DefaultProfiles)
	app.Port, app.ContextPath = serverInfo(e)
	if app.Port > 0 {
		app.URL = fmt.Sprintf("http://localhost:%d%s", app.Port, app.ContextPath)
	}
	app.Web = "servlet"
	for _, d := range app.Dependencies {
		if strings.Contains(d, "spring-boot-starter-actuator") || strings.Contains(d, "spring-boot-actuator") {
			app.Actuator = true
		}
		if strings.Contains(d, "spring-boot-starter-webflux") {
			app.Web = "reactive"
		}
	}
	app.Services = detectServices(e, app.Dependencies)
	if app.Actuator {
		// management.server.port 单独设置时端点在另一个端口上，且不带应用的 context-path
		base := firstNonEmpty(e.get("management.endpoints.web.base-path"), "/actuator")
		if mp := e.get("management.server.port"); mp != "" && mp != strconv.Itoa(app.Port) {
			app.ActuatorURL = "http://localhost:" + mp + strings.TrimRight(e.get("management.server.base-path"), "/") + base
		} else if app.Port > 0 {
			app.ActuatorURL = app.URL + base
		}
	}
}

func simpleName(fqcn string) string {
	if i := strings.LastIndexByte(fqcn, '.'); i >= 0 {
		return fqcn[i+1:]
	}
	return fqcn
}

func splitList(s string) []string {
	var out []string
	for _, x := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }) {
		if x != "" {
			out = append(out, x)
		}
	}
	return out
}

func sortStrings(xs []string) {
	for i := 1; i < len(xs); i++ {
		for j := i; j > 0 && xs[j] < xs[j-1]; j-- {
			xs[j], xs[j-1] = xs[j-1], xs[j]
		}
	}
}

// ideaModule 把 IDEA 模块名（vrtalk-api.vrtalk-api-starter.main）转成 Gradle 路径；Maven 直接是 artifactId。
func ideaModule(module string, bm *buildModel) string {
	if module == "" {
		return ""
	}
	if bm.Tool != "gradle" {
		return module
	}
	for _, suf := range []string{".main", ".test"} {
		module = strings.TrimSuffix(module, suf)
	}
	root := bm.RootName
	if module == root {
		return ""
	}
	module = strings.TrimPrefix(module, root+".")
	path := ":" + strings.ReplaceAll(module, ".", ":")
	for _, p := range bm.Projects {
		if p.Path == path || p.Name == module {
			return p.Path
		}
	}
	return path
}

// ---------------------------------------------------------------------------
// 运行

// ClasspathResult 是构建插件 prepareClasspath 服务的输出。
type ClasspathResult struct {
	Classpath  []string `json:"classpath"`
	MainClass  string   `json:"mainClass,omitempty"`
	WorkingDir string   `json:"workingDir,omitempty"`
}

// PrepareRun：direct 模式由构建插件编译并导出类路径，再调 java 插件的 buildCommand 生成 java 命令；
// buildTool 模式改用 bootRun / spring-boot:run。
func (p *Plugin) PrepareRun(ctx *sdk.Context, rp *sdk.PrepareRunParams) (*sdk.LaunchSpec, error) {
	if rp.Config.Type != typeBoot {
		return nil, sdk.ErrNotSupported
	}
	opts := rp.Config.Options
	main := sdk.OptString(opts, "mainClass", "")
	module := sdk.OptString(opts, "module", "")
	profiles := sdk.OptStrings(opts, "activeProfiles")
	port := optInt(opts, "port")
	tool := ""
	switch {
	case rp.Module.HasFacet("gradle"):
		tool = "gradle"
	case rp.Module.HasFacet("maven"):
		tool = "maven"
	default:
		return nil, sdk.UserError("%s", ctx.T("err.noBuildTool"))
	}
	var model Model
	rp.Module.Model("spring-boot", &model)
	app := findApp(&model, main, module)
	expectedPort := port
	if expectedPort == 0 && app != nil {
		docs := loadConfigs(app.ResourceDirs)
		active := profiles
		if len(active) == 0 {
			active = defaultProfiles(docs)
		}
		expectedPort, _ = serverInfo(merge(docs, active))
	}
	if sdk.OptString(opts, "launchMode", "direct") == "buildTool" {
		spec, err := p.buildToolLaunch(ctx, rp, tool, module, profiles, port)
		if err != nil {
			return nil, err
		}
		if expectedPort > 0 {
			spec.Ports = []int{expectedPort}
		}
		return spec, nil
	}
	if main == "" {
		return nil, sdk.UserError("%s", ctx.T("err.noMainClass"))
	}
	jdk, ok := rp.Toolchains["jdk"]
	if !ok {
		return nil, sdk.UserError("%s", ctx.T("err.noJdk"))
	}
	var cp ClasspathResult
	req := map[string]any{"project": rp.Project, "module": rp.Module, "toolchains": rp.Toolchains, "env": rp.Env, "runId": rp.RunID, "subproject": module}
	if err := ctx.Host.Call(ctx, tool, "prepareClasspath", req, &cp); err != nil {
		return nil, err
	}
	extra := []string{"-XX:TieredStopAtLevel=1", "-Dspring.output.ansi.enabled=always"}
	if len(profiles) > 0 {
		extra = append(extra, "-Dspring.profiles.active="+strings.Join(profiles, ","))
	}
	if port > 0 {
		extra = append(extra, "-Dserver.port="+strconv.Itoa(port))
	}
	cwd := sdk.OptString(opts, "workingDir", "")
	if cwd != "" && !filepath.IsAbs(cwd) {
		cwd = filepath.Join(rp.Project.Root, cwd)
	}
	if cwd == "" {
		cwd = firstNonEmpty(cp.WorkingDir, rp.Module.Dir)
	}
	var spec sdk.LaunchSpec
	if err := ctx.Host.Call(ctx, "java", "buildCommand", map[string]any{
		"jdk": jdk, "classpath": cp.Classpath, "mainClass": main,
		"vmOptions": sdk.OptString(opts, "vmOptions", ""), "programArgs": sdk.OptString(opts, "programArgs", ""),
		"extraVmArgs": extra, "cwd": cwd, "key": firstNonEmpty(rp.Config.ID, sdk.StableID(main)),
	}, &spec); err != nil {
		return nil, err
	}
	if expectedPort > 0 {
		spec.Ports = []int{expectedPort}
	}
	return &spec, nil
}

func (p *Plugin) buildToolLaunch(ctx *sdk.Context, rp *sdk.PrepareRunParams, tool, module string, profiles []string, port int) (*sdk.LaunchSpec, error) {
	env := map[string]string{}
	if len(profiles) > 0 {
		env["SPRING_PROFILES_ACTIVE"] = strings.Join(profiles, ",")
	}
	if port > 0 {
		env["SERVER_PORT"] = strconv.Itoa(port)
	}
	args := sdk.OptString(rp.Config.Options, "programArgs", "")
	vm := sdk.OptString(rp.Config.Options, "vmOptions", "")
	var spec sdk.LaunchSpec
	var err error
	if tool == "gradle" {
		task := "bootRun"
		if module != "" && module != ":" {
			task = module + ":bootRun"
		}
		var extra []string
		if args != "" {
			extra = append(extra, "--args="+args)
		}
		err = ctx.Host.Call(ctx, "gradle", "launchTasks", map[string]any{
			"project": rp.Project, "module": rp.Module, "toolchains": rp.Toolchains, "env": sdk.MergeEnv(rp.Env, env), "runId": rp.RunID,
			"tasks": []string{task}, "args": extra,
		}, &spec)
	} else {
		var extra []string
		if module != "" {
			extra = append(extra, "-pl", ":"+strings.TrimPrefix(module, ":"))
		}
		if len(profiles) > 0 {
			extra = append(extra, "-Dspring-boot.run.profiles="+strings.Join(profiles, ","))
		}
		jvm := vm
		if port > 0 {
			jvm = strings.TrimSpace(jvm + " -Dserver.port=" + strconv.Itoa(port))
		}
		if jvm != "" {
			extra = append(extra, "-Dspring-boot.run.jvmArguments="+jvm)
		}
		if args != "" {
			extra = append(extra, "-Dspring-boot.run.arguments="+args)
		}
		err = ctx.Host.Call(ctx, "maven", "launchGoals", map[string]any{
			"project": rp.Project, "module": rp.Module, "toolchains": rp.Toolchains, "env": sdk.MergeEnv(rp.Env, env), "runId": rp.RunID,
			"goals": []string{"spring-boot:run"}, "args": extra,
		}, &spec)
	}
	if err != nil {
		return nil, err
	}
	spec.Env = sdk.MergeEnv(spec.Env, env)
	return &spec, nil
}

func optInt(opts map[string]any, key string) int {
	switch v := opts[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(v))
		return n
	}
	return 0
}

func findApp(m *Model, main, module string) *App {
	for i := range m.Apps {
		if m.Apps[i].MainClass == main && (module == "" || m.Apps[i].Module == module || strings.TrimPrefix(m.Apps[i].Module, ":") == strings.TrimPrefix(module, ":")) {
			return &m.Apps[i]
		}
	}
	for i := range m.Apps {
		if m.Apps[i].MainClass == main {
			return &m.Apps[i]
		}
	}
	if len(m.Apps) == 1 && main == "" {
		return &m.Apps[0]
	}
	return nil
}

// ---------------------------------------------------------------------------
// 工具：apps

// CallTool 处理 apps 工具：列出应用、profile、端口与依赖的中间件。
func (p *Plugin) CallTool(ctx *sdk.Context, t *sdk.ToolCallParams) (any, error) {
	if t.Tool != "apps" {
		return nil, sdk.ErrNotSupported
	}
	if t.Module == nil {
		return nil, sdk.UserError("%s", ctx.T("err.noModule"))
	}
	var m Model
	if !t.Module.Model("spring-boot", &m) {
		res, err := p.Analyze(ctx, &sdk.AnalyzeParams{Module: *t.Module, Project: derefProject(t.Project)})
		if err != nil {
			return nil, err
		}
		_ = json.Unmarshal(res.Model, &m)
	}
	type appOut struct {
		Name            string    `json:"name"`
		MainClass       string    `json:"mainClass"`
		Module          string    `json:"module,omitempty"`
		Profiles        []string  `json:"profiles"`
		DefaultProfiles []string  `json:"defaultProfiles"`
		Port            int       `json:"port"`
		ContextPath     string    `json:"contextPath"`
		URL             string    `json:"url"`
		Actuator        bool      `json:"actuator"`
		Services        []Service `json:"services"`
	}
	out := struct {
		BootVersion string   `json:"bootVersion"`
		Apps        []appOut `json:"apps"`
	}{BootVersion: m.BootVersion}
	for _, a := range m.Apps {
		services := a.Services
		if services == nil {
			services = []Service{}
		}
		out.Apps = append(out.Apps, appOut{a.Name, a.MainClass, a.Module, nonNil(a.Profiles), nonNil(a.DefaultProfiles), a.Port, a.ContextPath, a.URL, a.Actuator, services})
	}
	return out, nil
}

func nonNil(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

func derefProject(p *sdk.ProjectInfo) sdk.ProjectInfo {
	if p == nil {
		return sdk.ProjectInfo{}
	}
	return *p
}
