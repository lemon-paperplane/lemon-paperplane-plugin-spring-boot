package springboot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/lemon-paperplane/lemon-paperplane-main/lemon-paperplane-main-plugin-sdk"
)

// healthOf 读取 /actuator/health 的 status（UP / DOWN / OUT_OF_SERVICE），连不上返回 ""。
func healthOf(ctx context.Context, base string) string {
	cctx, cancel := context.WithTimeout(ctx, 800*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, base+"/health", nil)
	if err != nil {
		return ""
	}
	resp, err := (&http.Client{Transport: &http.Transport{Proxy: nil}}).Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	var h struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(b, &h) == nil && h.Status != "" {
		return h.Status
	}
	return "HTTP " + strconv.Itoa(resp.StatusCode)
}

// panelApp 是面板上一个应用的展示数据。
type panelApp struct {
	app      App
	profiles []string
	url      string
	port     int
	actuator string
	services []Service
	running  bool
}

// RenderView 渲染运行面板：访问地址、生效 profile、端口，有 actuator 时给出 health / mappings 链接与健康状态。
// 有 Run 信息时只显示这次运行的应用，profile / 端口 / 地址用运行配置与内核识别到的实际值。
func (p *Plugin) RenderView(ctx *sdk.Context, v *sdk.ViewRenderParams) (*sdk.ViewRenderResult, error) {
	if v.View != "app" {
		return nil, sdk.ErrNotSupported
	}
	var model Model
	if v.Module != nil {
		v.Module.Model("spring-boot", &model)
	}
	panels := panelsFor(&model, v.Run)
	if len(panels) == 0 {
		return &sdk.ViewRenderResult{Root: sdk.Empty(ctx.T("view.noApps"), "")}, nil
	}
	health := make([]string, len(panels))
	var wg sync.WaitGroup
	for i, pa := range panels {
		if pa.actuator == "" {
			continue
		}
		wg.Add(1)
		go func(i int, u string) {
			defer wg.Done()
			health[i] = healthOf(ctx, u)
		}(i, pa.actuator)
	}
	wg.Wait()
	var sections []sdk.ViewNode
	for i, pa := range panels {
		profiles := strings.Join(pa.profiles, ", ")
		if profiles == "" {
			profiles = "default"
		}
		items := []sdk.KV{
			{Key: ctx.T("view.url"), Value: pa.url, Href: pa.url, Mono: true},
			{Key: ctx.T("view.profiles"), Value: profiles},
			{Key: ctx.T("view.port"), Value: strconv.Itoa(pa.port), Mono: true},
		}
		for _, s := range pa.services {
			items = append(items, sdk.KV{Key: s.Name, Value: s.Address, Mono: true})
		}
		kv := sdk.ViewNode{Type: "kv", Items: items}
		if !pa.running {
			kv.Info = ctx.T("view.defaultsInfo")
		}
		children := []sdk.ViewNode{kv}
		if pa.actuator != "" {
			tone, text := "neutral", ctx.T("view.unreachable")
			switch health[i] {
			case "":
			case "UP":
				tone, text = "success", "UP"
			default:
				tone, text = "danger", health[i]
			}
			children = append(children, sdk.Row(
				sdk.ViewNode{Type: "badge", Text: "Health " + text, Tone: tone},
				sdk.ViewNode{Type: "link", Text: "health", Href: pa.actuator + "/health"},
				sdk.ViewNode{Type: "link", Text: "mappings", Href: pa.actuator + "/mappings"},
			))
		}
		sections = append(sections, sdk.Section(pa.app.Name, children...))
	}
	return &sdk.ViewRenderResult{Root: sdk.Column(sections...), RefreshMs: 5000}, nil
}

// panelsFor 计算要展示的应用：有运行信息时按运行配置（profile、端口）重新读配置，并以内核识别到的 URL / 端口为准。
func panelsFor(model *Model, run *sdk.RunInfo) []panelApp {
	if run == nil || run.Config == nil {
		var out []panelApp
		for _, a := range model.Apps {
			out = append(out, panelApp{app: a, profiles: a.DefaultProfiles, url: a.URL, port: a.Port, actuator: a.ActuatorURL, services: a.Services})
		}
		return out
	}
	opts := run.Config.Options
	app := findApp(model, sdk.OptString(opts, "mainClass", ""), sdk.OptString(opts, "module", ""))
	if app == nil {
		return nil
	}
	a := *app
	profiles := sdk.OptStrings(opts, "activeProfiles")
	docs := loadConfigs(a.ResourceDirs)
	if len(profiles) == 0 {
		profiles = defaultProfiles(docs)
	}
	e := merge(docs, profiles)
	port, ctxPath := serverInfo(e)
	if n := optInt(opts, "port"); n > 0 {
		port = n
	}
	url := fmt.Sprintf("http://localhost:%d%s", port, ctxPath)
	if len(run.URLs) > 0 {
		url = strings.TrimRight(run.URLs[0], "/")
		if u, err := neturl.Parse(url); err == nil {
			if n, err := strconv.Atoi(u.Port()); err == nil {
				port = n
			}
		}
	} else if len(run.Ports) > 0 && optInt(opts, "port") == 0 {
		port = run.Ports[0]
		url = fmt.Sprintf("http://localhost:%d%s", port, ctxPath)
	}
	pa := panelApp{app: a, profiles: profiles, url: url, port: port, services: detectServices(e, a.Dependencies), running: run.State == "running"}
	if a.Actuator {
		base := firstNonEmpty(e.get("management.endpoints.web.base-path"), "/actuator")
		if mp := e.get("management.server.port"); mp != "" && mp != strconv.Itoa(port) {
			pa.actuator = "http://localhost:" + mp + strings.TrimRight(e.get("management.server.base-path"), "/") + base
		} else {
			pa.actuator = url + base
		}
	}
	return []panelApp{pa}
}

// HandleViewEvent：面板没有交互事件，统一刷新。
func (p *Plugin) HandleViewEvent(ctx *sdk.Context, e *sdk.ViewEventParams) (*sdk.ViewEventResult, error) {
	return &sdk.ViewEventResult{Refresh: true}, nil
}
