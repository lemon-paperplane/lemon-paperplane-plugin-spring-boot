package springboot

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	sdk "github.com/lemon-paperplane/lemon-paperplane-main/lemon-paperplane-main-plugin-sdk"
)

// buildProject 是 gradle / maven 插件模型里的工程（两者字段对齐）。
type buildProject struct {
	Path                string   `json:"path"`
	Name                string   `json:"name"`
	Dir                 string   `json:"dir"`
	RelDir              string   `json:"relDir"`
	Java                bool     `json:"java"`
	SpringBoot          bool     `json:"springBoot"`
	SpringBootPlugin    bool     `json:"springBootPlugin"`
	SpringBootVersion   string   `json:"springBootVersion"`
	SpringBootMainClass string   `json:"springBootMainClass"`
	Kotlin              bool     `json:"kotlin"`
	Android             bool     `json:"android"`
	Packaging           string   `json:"packaging"`
	Plugins             []string `json:"plugins"`
	Dependencies        []string `json:"dependencies"`
	SourceDirs          []string `json:"sourceDirs"`
	ResourceDirs        []string `json:"resourceDirs"`
}

type buildModel struct {
	Tool     string         `json:"tool"`
	RootName string         `json:"rootName"`
	Synced   bool           `json:"synced"`
	Projects []buildProject `json:"projects"`
}

// readBuildModel 取 gradle / maven 插件的模型；都没有时按单工程处理。
func readBuildModel(m *sdk.ModuleInfo) *buildModel {
	for _, id := range []string{"gradle", "maven"} {
		var bm buildModel
		if m.Model(id, &bm) && len(bm.Projects) > 0 {
			bm.Tool = id
			return &bm
		}
	}
	// 没有构建插件模型：从 facet 数据里拿子工程清单
	bm := &buildModel{Tool: "none"}
	for _, id := range []string{"gradle", "maven"} {
		f := m.Facet(id)
		if f == nil {
			continue
		}
		bm.Tool = id
		var data struct {
			Projects []buildProject `json:"projects"`
		}
		if b, err := json.Marshal(f.Data); err == nil {
			_ = json.Unmarshal(b, &data)
		}
		for _, p := range data.Projects {
			p.RelDir = p.Dir
			p.Dir = filepath.Join(m.Dir, filepath.FromSlash(p.Dir))
			p.Java = true
			bm.Projects = append(bm.Projects, p)
		}
	}
	if len(bm.Projects) == 0 {
		bm.Projects = []buildProject{{Path: "", Dir: m.Dir, Java: true}}
	}
	return bm
}

// App 是识别到的一个 Spring Boot 应用。
type App struct {
	Name            string    `json:"name"`
	MainClass       string    `json:"mainClass"`
	Module          string    `json:"module,omitempty"` // Gradle 路径 / Maven artifactId
	ModuleDir       string    `json:"moduleDir"`
	Kotlin          bool      `json:"kotlin,omitempty"`
	BootVersion     string    `json:"bootVersion,omitempty"`
	Profiles        []string  `json:"profiles,omitempty"`
	DefaultProfiles []string  `json:"defaultProfiles,omitempty"`
	Port            int       `json:"port"`
	ContextPath     string    `json:"contextPath,omitempty"`
	URL             string    `json:"url,omitempty"`
	Actuator        bool      `json:"actuator,omitempty"`
	ActuatorURL     string    `json:"actuatorUrl,omitempty"` // 例如 http://localhost:8080/api/actuator
	Web             string    `json:"web,omitempty"`         // servlet | reactive | none
	Services        []Service `json:"services,omitempty"`
	ResourceDirs    []string  `json:"resourceDirs,omitempty"`
	Dependencies    []string  `json:"dependencies,omitempty"`
	BootPlugin      bool      `json:"bootPlugin,omitempty"`
}

var (
	packageRe       = regexp.MustCompile(`(?m)^\s*package\s+([\w.]+)`)
	javaMainRe      = regexp.MustCompile(`\bstatic\s+(?:final\s+)?void\s+main\s*\(`)
	ktTopMainRe     = regexp.MustCompile(`(?m)^(?:public\s+|suspend\s+)*fun\s+main\s*\(`)
	ktJvmStaticRe   = regexp.MustCompile(`@JvmStatic\s+(?:public\s+)?fun\s+main\s*\(`)
	ktJvmNameRe     = regexp.MustCompile(`@file:JvmName\(\s*"([\w$]+)"\s*\)`)
	ktClassAfterAnn = regexp.MustCompile(`@SpringBootApplication[\s\S]*?\bclass\s+(\w+)`)
)

// scanApps 在各工程源码里找 @SpringBootApplication（或 @SpringBootConfiguration + @EnableAutoConfiguration）且带 main 的类。
func scanApps(bm *buildModel) []App {
	var apps []App
	files := 0
	for _, pr := range bm.Projects {
		if pr.Android {
			continue
		}
		dirs := pr.SourceDirs
		if len(dirs) == 0 {
			dirs = []string{filepath.Join(pr.Dir, "src", "main", "java"), filepath.Join(pr.Dir, "src", "main", "kotlin")}
		}
		for _, sd := range dirs {
			if !sdk.DirExists(sd) {
				continue
			}
			_ = filepath.WalkDir(sd, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return nil
				}
				if d.IsDir() {
					if path != sd && (strings.HasPrefix(d.Name(), ".") || d.Name() == "test") {
						return fs.SkipDir
					}
					return nil
				}
				isKt := strings.HasSuffix(d.Name(), ".kt")
				if !isKt && !strings.HasSuffix(d.Name(), ".java") {
					return nil
				}
				files++
				if files > 30000 || len(apps) >= 30 {
					return fs.SkipAll
				}
				if st, err := d.Info(); err != nil || st.Size() > 1<<20 {
					return nil
				}
				data, err := os.ReadFile(path)
				if err != nil {
					return nil
				}
				if cls := bootMainClass(data, d.Name(), isKt); cls != "" {
					apps = append(apps, App{MainClass: cls, Module: pr.Path, ModuleDir: pr.Dir, Kotlin: isKt, BootVersion: pr.SpringBootVersion,
						ResourceDirs: resourceDirs(pr), Dependencies: pr.Dependencies, BootPlugin: pr.SpringBootPlugin || contains(pr.Plugins, "org.springframework.boot")})
				}
				return nil
			})
		}
	}
	sort.SliceStable(apps, func(i, j int) bool {
		if apps[i].BootPlugin != apps[j].BootPlugin {
			return apps[i].BootPlugin
		}
		return apps[i].MainClass < apps[j].MainClass
	})
	return apps
}

func resourceDirs(pr buildProject) []string {
	if len(pr.ResourceDirs) > 0 {
		return pr.ResourceDirs
	}
	return []string{filepath.Join(pr.Dir, "src", "main", "resources")}
}

// bootMainClass 判断源文件是否是 Spring Boot 启动类，返回运行时主类名。
func bootMainClass(data []byte, file string, kotlin bool) string {
	isBoot := bytes.Contains(data, []byte("@SpringBootApplication")) ||
		(bytes.Contains(data, []byte("@SpringBootConfiguration")) && bytes.Contains(data, []byte("@EnableAutoConfiguration")))
	if !isBoot {
		return ""
	}
	pkg := ""
	if m := packageRe.FindSubmatch(data); m != nil {
		pkg = string(m[1]) + "."
	}
	base := strings.TrimSuffix(strings.TrimSuffix(file, ".java"), ".kt")
	if !kotlin {
		if !javaMainRe.Match(data) {
			return ""
		}
		return pkg + base
	}
	// Kotlin：顶层 fun main 编译进文件门面类 <文件名>Kt（@file:JvmName 可改名）；伴生对象 @JvmStatic main 则是类本身
	if ktTopMainRe.Match(data) {
		if m := ktJvmNameRe.FindSubmatch(data); m != nil {
			return pkg + string(m[1])
		}
		return pkg + kotlinFacade(base)
	}
	if ktJvmStaticRe.Match(data) {
		if m := ktClassAfterAnn.FindSubmatch(data); m != nil {
			return pkg + string(m[1])
		}
	}
	return ""
}

// kotlinFacade 计算 Kotlin 文件门面类名：首字母大写、非标识符字符换成 _，再加 Kt。
func kotlinFacade(base string) string {
	var b strings.Builder
	for i, r := range base {
		ok := r == '_' || r == '$' || (r >= '0' && r <= '9' && i > 0) || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r > 127
		if !ok {
			r = '_'
		}
		if i == 0 && r >= 'a' && r <= 'z' {
			r = r - 'a' + 'A'
		}
		b.WriteRune(r)
	}
	return b.String() + "Kt"
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// bootVersionOf 汇总模块里的 Spring Boot 版本（取最高）。
func bootVersionOf(bm *buildModel) string {
	var vs []string
	for _, p := range bm.Projects {
		if p.SpringBootVersion != "" && !strings.Contains(p.SpringBootVersion, "$") {
			vs = append(vs, p.SpringBootVersion)
		}
	}
	if len(vs) == 0 {
		return ""
	}
	sdk.SortVersionsDesc(vs)
	return vs[0]
}

// jdkConstraint：Spring Boot 版本对 JDK 的要求（Boot 3/4 需要 17+；老版本的 Spring Framework 不能跑在太新的 JDK 上）。
func jdkConstraint(boot string) string {
	v, ok := sdk.ParseVersion(boot)
	if !ok {
		return ""
	}
	switch {
	case v.Major() >= 3:
		return ">=17"
	case v.Major() == 2 && v.Minor() >= 4:
		return ">=8 <22"
	case v.Major() == 2:
		return ">=8 <16"
	case v.Major() == 1:
		return ">=8 <12"
	}
	return ""
}
