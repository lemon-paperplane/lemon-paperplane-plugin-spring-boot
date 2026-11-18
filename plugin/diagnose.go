package springboot

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"

	sdk "github.com/lemon-paperplane/lemon-paperplane-main/lemon-paperplane-main-plugin-sdk"
)

var (
	portInUseRe = regexp.MustCompile(`Port (\d+) was already in use`)
	logLineRe   = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}[ T]\d{2}:\d{2}`)
)

// failureReport 是 Spring Boot FailureAnalyzer 输出的「APPLICATION FAILED TO START」段落。
type failureReport struct {
	Description string
	Action      string
}

// parseFailure 从日志末尾解析 Description / Action。
func parseFailure(lines []string) *failureReport {
	start := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.Contains(lines[i], "APPLICATION FAILED TO START") {
			start = i
			break
		}
	}
	if start < 0 {
		return nil
	}
	r := &failureReport{}
	section := ""
	var desc, action []string
	for _, l := range lines[start+1:] {
		t := strings.TrimSpace(l)
		switch {
		case t == "Description:":
			section = "d"
			continue
		case t == "Action:":
			section = "a"
			continue
		case strings.HasPrefix(t, "****"):
			continue
		case section == "a" && len(action) > 0 && (t == "" || logLineRe.MatchString(t)):
			// Action 只有一段，空行之后是别的输出
			section = ""
		}
		switch section {
		case "d":
			desc = append(desc, l)
		case "a":
			action = append(action, l)
		}
	}
	r.Description = strings.TrimSpace(strings.Join(desc, "\n"))
	r.Action = strings.TrimSpace(strings.Join(action, "\n"))
	if r.Description == "" {
		return nil
	}
	return r
}

// firstSentence 取描述的第一句作为标题。
func firstSentence(s string) string {
	s = strings.TrimSpace(strings.SplitN(s, "\n", 2)[0])
	if i := strings.Index(s, ". "); i > 0 {
		s = s[:i+1]
	}
	if len([]rune(s)) > 140 {
		s = string([]rune(s)[:140]) + "…"
	}
	return s
}

// 运行中才会报错的中间件，由声明式规则（manifest）先行命中
var runtimeRuleIDs = map[string]string{
	"redis": "spring-boot.redis", "kafka": "spring-boot.kafka", "rabbitmq": "spring-boot.rabbitmq",
	"elasticsearch": "spring-boot.elasticsearch", "mongodb": "spring-boot.mongodb",
}

// Diagnose 解析 FailureAnalyzer 报告；识别连不上的中间件并给出配置里写的地址。
func (p *Plugin) Diagnose(ctx *sdk.Context, d *sdk.DiagnoseParams) (*sdk.DiagnoseResult, error) {
	if d.Stage != "" && d.Stage != "run" {
		return &sdk.DiagnoseResult{}, nil
	}
	matched := map[string]bool{}
	for _, m := range d.Matched {
		matched[m.RuleID] = true
	}
	var model Model
	d.Module.Model("spring-boot", &model)
	var app *App
	var profiles []string
	if d.Config != nil {
		app = findApp(&model, sdk.OptString(d.Config.Options, "mainClass", ""), sdk.OptString(d.Config.Options, "module", ""))
		profiles = sdk.OptStrings(d.Config.Options, "activeProfiles")
	} else if len(model.Apps) > 0 {
		app = &model.Apps[0]
	}
	var docs []configDoc
	if app != nil {
		docs = loadConfigs(app.ResourceDirs)
		if len(profiles) == 0 {
			profiles = defaultProfiles(docs)
		}
	}
	e := merge(docs, profiles)
	var out []sdk.Diagnosis

	if fr := parseFailure(d.LogTail); fr != nil {
		diag := sdk.Diagnosis{Severity: "error", Title: firstSentence(fr.Description), Message: ctx.T("diag.failed.message"),
			Detail: "Description:\n" + fr.Description + "\n\nAction:\n" + fr.Action}
		switch {
		case portInUseRe.MatchString(fr.Description):
			port := portInUseRe.FindStringSubmatch(fr.Description)[1]
			diag.Title = ctx.T("diag.portInUse.title", port)
			// 同端口的多条诊断由内核合并，这里给出完整的修复项
			diag.Fixes = []sdk.Fix{
				{ID: "killPort", Title: ctx.T("fix.killPort"), Kind: sdk.FixKillPort, Params: map[string]string{"port": port}, Confirm: ctx.T("fix.killPort.confirm", port), Recommended: true},
				{ID: "useOtherPort", Title: ctx.T("fix.useOtherPort"), Kind: sdk.FixCustom, Params: map[string]string{"port": port}},
			}
		case strings.Contains(fr.Description, "Failed to configure a DataSource"):
			diag.Title = ctx.T("diag.dataSource.title")
			diag.Message = ctx.T("diag.dataSource.message")
			diag.Fixes = profileFixes(ctx, docs, profiles, "spring.datasource.url")
		}
		out = append(out, diag)
	}

	// 连不上的中间件
	services := detectServices(e, appDeps(app))
	seen := map[string]bool{}
	for _, sig := range failureSignatures {
		if seen[sig.kind] || matched[runtimeRuleIDs[sig.kind]] {
			continue
		}
		hit := ""
		for _, l := range d.LogTail {
			if sig.re.MatchString(l) {
				hit = l
				break
			}
		}
		if hit == "" {
			continue
		}
		seen[sig.kind] = true
		name := serviceNames[sig.kind]
		diag := sdk.Diagnosis{Severity: "error", Title: ctx.T("diag.service.title", name), Line: strings.TrimSpace(hit)}
		if s := findService(services, sig.kind); s != nil {
			src := s.Source
			if src == "" || s.Default {
				src = ctx.T("diag.service.defaultSource")
			}
			diag.Message = ctx.T("diag.service.message", name, s.Address, s.Key, src)
			diag.Fixes = profileFixes(ctx, docs, profiles, s.Key)
		} else {
			diag.Message = ctx.T("diag.service.unknown", name)
		}
		if sig.kind == "mysql" && strings.Contains(hit, "Access denied") {
			diag.Title = ctx.T("diag.dbAuth.title", name)
		}
		out = append(out, diag)
	}
	return &sdk.DiagnoseResult{Diagnoses: out}, nil
}

func appDeps(app *App) []string {
	if app == nil {
		return nil
	}
	return app.Dependencies
}

func findService(ss []Service, kind string) *Service {
	for i := range ss {
		if ss[i].Kind == kind || (kind == "mysql" && ss[i].Kind == "mariadb") {
			return &ss[i]
		}
	}
	return nil
}

// profileFixes：给出其他 profile 下这个配置键有不同值时的「改用 profile X」修复（最多 3 个）。
func profileFixes(ctx *sdk.Context, docs []configDoc, current []string, key string) []sdk.Fix {
	if len(docs) == 0 {
		return nil
	}
	cur := merge(docs, current).get(key)
	var fixes []sdk.Fix
	for _, pf := range profilesOf(docs) {
		if containsStr(current, pf) {
			continue
		}
		v := merge(docs, []string{pf}).get(key)
		if v == "" || v == cur {
			continue
		}
		fixes = append(fixes, sdk.Fix{ID: "profile-" + pf, Title: ctx.T("fix.useProfile", pf), Kind: sdk.FixSetRunOption, Params: map[string]string{"key": "activeProfiles", "value": pf}})
		if len(fixes) >= 3 {
			break
		}
	}
	if len(fixes) == 0 && len(profilesOf(docs)) > 0 {
		fixes = append(fixes, sdk.Fix{ID: "chooseProfile", Title: ctx.T("fix.chooseProfile"), Kind: sdk.FixCustom})
	}
	return fixes
}

func containsStr(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// 修复

// ApplyFix 处理 custom 修复：换一个空闲端口；选择 profile。
func (p *Plugin) ApplyFix(ctx *sdk.Context, f *sdk.ApplyFixParams) (*sdk.ApplyFixResult, error) {
	switch f.Fix.ID {
	case "useOtherPort":
		base, _ := strconv.Atoi(f.Fix.Params["port"])
		if base == 0 {
			base = 8080
		}
		port := freePort(base + 1)
		if port == 0 {
			return nil, sdk.UserError("%s", ctx.T("err.noFreePort"))
		}
		return &sdk.ApplyFixResult{Changes: &sdk.ProjectChanges{RunOptions: map[string]any{"port": port}}, Rerun: true, Message: ctx.T("fix.portSet", port)}, nil
	case "chooseProfile":
		var model Model
		f.Module.Model("spring-boot", &model)
		var opts []sdk.Option
		for _, pf := range model.Profiles {
			opts = append(opts, sdk.Option{Value: pf})
		}
		if len(opts) == 0 {
			return &sdk.ApplyFixResult{Message: ctx.T("fix.noProfiles")}, nil
		}
		r, err := ctx.Host.Prompt(ctx, &sdk.PromptParams{
			Title:  ctx.T("fix.chooseProfile"),
			Fields: []sdk.FormField{{Key: "activeProfiles", Title: ctx.T("opt.activeProfiles"), Type: "multiselect", Options: opts}},
		})
		if err != nil || r.Cancelled {
			names := make([]string, 0, len(opts))
			for _, o := range opts {
				names = append(names, o.Value)
			}
			return &sdk.ApplyFixResult{Message: ctx.T("fix.availableProfiles", strings.Join(names, ", "))}, nil
		}
		return &sdk.ApplyFixResult{Changes: &sdk.ProjectChanges{RunOptions: map[string]any{"activeProfiles": r.Values["activeProfiles"]}}, Rerun: true}, nil
	}
	return nil, sdk.ErrNotSupported
}

// freePort 从 start 起找一个能监听的端口。
func freePort(start int) int {
	for p := start; p < start+200 && p < 65536; p++ {
		l, err := net.Listen("tcp", fmt.Sprintf(":%d", p))
		if err == nil {
			l.Close()
			return p
		}
	}
	return 0
}
