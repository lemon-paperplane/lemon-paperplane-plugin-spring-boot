# lemon-paperplane-plugin-spring-boot

Lemon Paperplane 的 Spring Boot 插件（依赖 java，可选 gradle / maven / kotlin）。

- 识别：Gradle 的 `org.springframework.boot` 插件或依赖、Maven 的 Boot 父 POM / 依赖；Boot 版本决定 JDK 需求（3.x/4.x 需要 17+，老版本限定上限）。
- 在各子工程源码里找 `@SpringBootApplication`（及 `@SpringBootConfiguration` + `@EnableAutoConfiguration`）且带 main 的类，每个应用一条运行配置；导入 IDEA 的 Spring Boot 运行配置（模块名 `root.sub.main` → `:sub`）。
- 读 `application*.yml|yaml|properties` 与 `bootstrap*`（多文档、`on-profile`、`${KEY:默认}` 占位符、宽松绑定）：profile 列表、默认 `spring.profiles.active`、端口与 context-path、各中间件地址。
- 运行：direct（默认）= 构建插件编译取类路径 + java 插件生成命令，默认 `-XX:TieredStopAtLevel=1 -Dspring.output.ansi.enabled=always`，profile 用 `-Dspring.profiles.active`；buildTool = bootRun / spring-boot:run。
- URL / 就绪：Tomcat / Jetty / Undertow / Netty 的「started on port」行（Boot 2/3 两种格式、https），`Started X in N seconds`。
- 诊断：解析「APPLICATION FAILED TO START」的 Description / Action；端口占用（结束进程 / 换空闲端口）；没配数据源（改用某个 profile）；连不上 MySQL / PostgreSQL / Redis / RabbitMQ / Kafka / Elasticsearch / MongoDB / Nacos 时给出配置里写的地址与来源文件。
- 运行面板 `app`（run.panel）：访问地址、profile、端口、中间件地址、actuator health / mappings。MCP 工具 `apps`：列出应用、profile、端口与中间件。
