package springboot

import (
	"regexp"
	"strings"
)

// Service 是应用依赖的一个外部中间件及其在配置里的地址。
type Service struct {
	Kind    string `json:"kind"`             // mysql | postgresql | oracle | sqlserver | mariadb | redis | rabbitmq | kafka | elasticsearch | mongodb | nacos
	Name    string `json:"name"`             // 展示名
	Address string `json:"address"`          // 配置里写的地址
	Key     string `json:"key,omitempty"`    // 配置键
	Source  string `json:"source,omitempty"` // 来源文件
	Default bool   `json:"default,omitempty"`
}

var jdbcRe = regexp.MustCompile(`^jdbc:(?:p6spy:|log4jdbc:)?([a-z0-9]+)(?::[a-z]+)*:(?://)?([^/;?]*)`)

var serviceNames = map[string]string{
	"mysql": "MySQL", "mariadb": "MariaDB", "postgresql": "PostgreSQL", "oracle": "Oracle", "sqlserver": "SQL Server",
	"redis": "Redis", "rabbitmq": "RabbitMQ", "kafka": "Kafka", "elasticsearch": "Elasticsearch", "mongodb": "MongoDB", "nacos": "Nacos",
	"h2": "H2",
}

// jdbcService 由 JDBC URL 推断数据库类型与主机。
func jdbcService(url string) (string, string) {
	m := jdbcRe.FindStringSubmatch(strings.TrimSpace(url))
	if m == nil {
		return "", ""
	}
	kind := m[1]
	switch kind {
	case "postgres":
		kind = "postgresql"
	case "microsoft", "jtds":
		kind = "sqlserver"
	}
	host := m[2]
	if kind == "oracle" {
		if i := strings.LastIndex(url, "@"); i >= 0 {
			host = strings.TrimPrefix(url[i+1:], "//")
		}
	}
	return kind, host
}

// detectServices 从生效配置与依赖里找出应用会连接的中间件。
func detectServices(e *effective, deps []string) []Service {
	has := func(sub ...string) bool {
		for _, d := range deps {
			for _, s := range sub {
				if strings.Contains(d, s) {
					return true
				}
			}
		}
		return false
	}
	var out []Service
	add := func(kind, addr, key string, def bool) {
		if addr == "" {
			return
		}
		for _, s := range out {
			if s.Kind == kind && s.Address == addr {
				return
			}
		}
		out = append(out, Service{Kind: kind, Name: serviceNames[kind], Address: addr, Key: key, Source: e.src(key), Default: def})
	}
	// 数据源：标准写法、Druid、dynamic-datasource 的主库
	for _, key := range []string{"spring.datasource.url", "spring.datasource.druid.url", "spring.datasource.hikari.jdbc-url", "spring.datasource.dynamic.datasource.master.url", "spring.datasource.master.url"} {
		if url := e.get(key); url != "" {
			if kind, host := jdbcService(url); kind != "" && kind != "h2" && kind != "hsqldb" && kind != "derby" {
				add(kind, url, key, false)
				_ = host
			}
		}
	}
	// Redis：Boot 3 用 spring.data.redis.*，Boot 2 用 spring.redis.*
	if has("redis", "redisson") || e.has("spring.data.redis.host") || e.has("spring.redis.host") {
		if u := firstNonEmpty(e.get("spring.data.redis.url"), e.get("spring.redis.url")); u != "" {
			add("redis", u, "spring.data.redis.url", false)
		} else {
			hostKey, portKey := "spring.data.redis.host", "spring.data.redis.port"
			if !e.has(hostKey) && e.has("spring.redis.host") {
				hostKey, portKey = "spring.redis.host", "spring.redis.port"
			}
			host, port := e.get(hostKey), e.get(portKey)
			def := host == ""
			add("redis", firstNonEmpty(host, "localhost")+":"+firstNonEmpty(port, "6379"), hostKey, def)
		}
	}
	if has("spring-boot-starter-amqp", "spring-rabbit", "com.rabbitmq") || e.has("spring.rabbitmq.host") || e.has("spring.rabbitmq.addresses") {
		if a := e.get("spring.rabbitmq.addresses"); a != "" {
			add("rabbitmq", a, "spring.rabbitmq.addresses", false)
		} else {
			host, port := e.get("spring.rabbitmq.host"), e.get("spring.rabbitmq.port")
			add("rabbitmq", firstNonEmpty(host, "localhost")+":"+firstNonEmpty(port, "5672"), "spring.rabbitmq.host", host == "")
		}
	}
	if has("spring-kafka", "kafka-clients") || e.has("spring.kafka.bootstrap-servers") {
		v := e.get("spring.kafka.bootstrap-servers")
		add("kafka", firstNonEmpty(v, "localhost:9092"), "spring.kafka.bootstrap-servers", v == "")
	}
	if has("elasticsearch") || e.has("spring.elasticsearch.uris") || e.has("spring.elasticsearch.rest.uris") {
		key := "spring.elasticsearch.uris"
		v := e.get(key)
		if v == "" {
			key = "spring.elasticsearch.rest.uris"
			v = e.get(key)
		}
		add("elasticsearch", firstNonEmpty(v, "http://localhost:9200"), key, v == "")
	}
	if has("mongodb") || e.has("spring.data.mongodb.uri") || e.has("spring.data.mongodb.host") {
		if u := e.get("spring.data.mongodb.uri"); u != "" {
			add("mongodb", u, "spring.data.mongodb.uri", false)
		} else {
			host, port := e.get("spring.data.mongodb.host"), e.get("spring.data.mongodb.port")
			add("mongodb", firstNonEmpty(host, "localhost")+":"+firstNonEmpty(port, "27017"), "spring.data.mongodb.host", host == "")
		}
	}
	for _, key := range []string{"spring.cloud.nacos.server-addr", "spring.cloud.nacos.discovery.server-addr", "spring.cloud.nacos.config.server-addr"} {
		if v := e.get(key); v != "" {
			add("nacos", v, key, false)
		}
	}
	if has("nacos") && !hasKind(out, "nacos") {
		add("nacos", "localhost:8848", "spring.cloud.nacos.server-addr", true)
	}
	return out
}

func hasKind(ss []Service, kind string) bool {
	for _, s := range ss {
		if s.Kind == kind {
			return true
		}
	}
	return false
}

// failureSignatures：日志里表示「连不上某个中间件」的特征串。
var failureSignatures = []struct {
	kind string
	re   *regexp.Regexp
}{
	{"mysql", regexp.MustCompile(`Communications link failure|CommunicationsException|Access denied for user|Unknown database|jdbc:mysql.*Connection refused`)},
	{"mariadb", regexp.MustCompile(`org\.mariadb\.jdbc.*(?:Connection refused|Could not connect)|Socket fail to connect to host`)},
	{"postgresql", regexp.MustCompile(`PSQLException: Connection to .* refused|PSQLException: FATAL`)},
	{"oracle", regexp.MustCompile(`ORA-12541|ORA-12514|ORA-01017|IO Error: The Network Adapter could not establish the connection`)},
	{"sqlserver", regexp.MustCompile(`SQLServerException: The TCP/IP connection to the host`)},
	{"redis", regexp.MustCompile(`RedisConnectionException|RedisConnectionFailureException|Unable to connect to Redis`)},
	{"rabbitmq", regexp.MustCompile(`AmqpConnectException|com\.rabbitmq\.client.*Connection refused`)},
	{"kafka", regexp.MustCompile(`Broker may not be available|could not be established\. Broker`)},
	{"elasticsearch", regexp.MustCompile(`co\.elastic\.clients.*(?:Connection refused|ConnectException)|ElasticsearchException|elasticsearch.*Connection refused`)},
	{"mongodb", regexp.MustCompile(`MongoSocketOpenException|MongoTimeoutException`)},
	{"nacos", regexp.MustCompile(`(?i)nacos.*(?:Client not connected|failed to req API|Connection refused|no available server|server status is DOWN|NacosException)`)},
}
