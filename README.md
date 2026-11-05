# CLI Proxy API

English | [中文](README_CN.md) | [日本語](README_JA.md)

If you want to use CLIProxyAPI on your desktop, we recommend our [EasyCLIProxyAPI](https://github.com/router-for-me/EasyCLIProxyAPI) desktop client. It provides a graphical configuration UI, automatic updates, system tray integration, and one-click start/stop for the CLIProxyAPI service.

CLIProxyAPI is a proxy server that provides OpenAI/Gemini/Claude/Codex/Grok compatible API interfaces for CLI.

You can access the following providers locally and with multiple CLI accounts through any OpenAI (including Responses), Gemini (including Interactions), or Claude-compatible client or SDK.

<table>
<tbody>
    <tr>
        <th align="center" width="100">Provider</th>
        <th align="center">Description</th>
    </tr>
    <tr>
        <td align="center"><a href="https://www.kimi.com/code/?aff=cliproxyapi"><img src="./assets/logo/kimi.svg" alt="Kimi" width="28" height="28" /></a></td>
        <td>Kimi series models (Kimi K3, K2.8 Preview, etc.). <a href="https://platform.kimi.ai/docs/guide/kimi-k3-quickstart">Kimi K3</a> is Moonshot AI’s most capable model and the world’s first open 3T-class model. With 2.8 trillion parameters, native vision, and a 1-million-token context window, K3 is built for long-horizon coding, knowledge work, and reasoning. CLIProxyAPI supports Kimi through OAuth or compatible API interfaces. Try a <strong>Kimi Code plan</strong> (<a href="https://www.kimi.com/code?aff=cliproxyapi">中文站</a> | <a href="https://www.kimi.ai/code?aff=cliproxyapi">Global</a>), or get an <strong>API key</strong> from the Kimi Open Platform (<a href="https://platform.kimi.com?track_id=track-f15622e7182046baa22ca35e006e13a7&aff=cliproxyapi">中文站</a> | <a href="https://platform.kimi.ai?track_id=track-8a28e4b291d84f62af2fccc3e7a21cb3&aff=cliproxyapi">Global</a>). Thanks to Kimi for supporting CLIProxyAPI and the open-source community!</td>
    </tr>
    <tr>
        <td align="center"><a href="https://developers.openai.com/api/docs/models"><img src="./assets/logo/openai.svg" alt="OpenAI" width="28" height="28" /></a></td>
        <td>OpenAI GPT-6 models (GPT-6 Astra, GPT-6.1 Sol, GPT-6 Luna), including access through Codex OAuth. Astra is the flagship for complex reasoning and coding, Sol balances capability and cost, and Luna handles focused, high-volume work.</td>
    </tr>
    <tr>
        <td align="center"><a href="https://www.anthropic.com/claude/fable"><img src="./assets/logo/claude.svg" alt="Anthropic" width="28" height="28" /></a></td>
        <td>Anthropic Claude models (Claude Fable 5.1, Claude Opus 5.5, Claude Sonnet 5.5, Claude Haiku 5.5). Fable 5.1 is built for ambitious, long-running coding and knowledge work; Opus 5.5 brings strong agentic coding at a lower cost.</td>
    </tr>
    <tr>
        <td align="center"><a href="https://antigravity.google/"><img src="./assets/logo/antigravity.svg" alt="Antigravity" width="28" height="28" /></a></td>
        <td>Google Gemini models include Gemini 4 Argon and Gemini 3.8 Flash. CLIProxyAPI supports Gemini API, AI Studio, Vertex AI, Gemini CLI, and Antigravity accounts; model availability varies by channel. Gemini 3.8 Flash is Google's latest Flash model for long-horizon software engineering and agentic workflows. Antigravity's <a href="https://antigravity.google/docs/models/">official model availability</a> lists Claude Sonnet 5.5 (thinking) and Claude Opus 5.5 (thinking) for Google AI Pro (non-trial), Google AI Ultra, and Enterprise.</td>
    </tr>
    <tr>
        <td align="center"><a href="https://docs.x.ai/developers/grok-4-7"><img src="./assets/logo/xai.svg" alt="xAI" width="28" height="28" /></a></td>
        <td>xAI Grok models (Grok 4.7, Grok 4.7 Build Fast, etc.). Grok 4.7 is SpaceXAI's latest model for coding, agentic tasks, and knowledge work.</td>
    </tr>
    <tr>
        <td align="center"><a href="https://dev.meta.ai/docs/overview"><img src="./assets/logo/meta.svg" alt="Meta" width="28" height="28" /></a></td>
        <td>Meta Muse models (Muse Wonder Full, Muse Wonder, Muse Spark 1.3, etc.). CLIProxyAPI supports Muse Code accounts through Meta login and Meta Model API keys, with Muse Spark 1.3 for coding and agentic workflows. Meta has announced Muse Wonder for Meta Model API and Muse Code, and Muse Wonder Full for Muse Code subscribers.</td>
    </tr>
    <tr>
        <td align="center"><a href="https://devin.ai/cli">Devin</a></td>
        <td>Devin models (SWE-2, GPT-6 Astra, Claude Fable 5.1, etc.). Connect a Devin account with <code>--devin-login</code> to route the models available to that account.</td>
    </tr>
</tbody>
</table>


## Sponsor

[![https://www.packyapi.com/register?aff=cliproxyapi](./assets/packycode-en.png)](https://www.packyapi.com/register?aff=cliproxyapi)

Thanks to PackyCode for sponsoring this project!

PackyCode is a reliable and efficient API relay service provider, offering relay services for Claude Code, Codex, Gemini, and more.

PackyCode provides special discounts for our software users: register using <a href="https://www.packyapi.com/register?aff=cliproxyapi">this link</a> and enter the "cliproxyapi" promo code during recharge to get 10% off.

---

<table>
<tbody>
<tr>
<td width="180"><a href="https://www.aicodemirror.ai/register?invitecode=TJNAIF"><img src="./assets/aicodemirror.png" alt="AICodeMirror" width="150"></a></td>
<td>Thanks to AICodeMirror for sponsoring this project! AICodeMirror provides official high-stability relay services for Claude Code / Codex / Gemini, with enterprise-grade concurrency, fast invoicing, and 24/7 dedicated technical support. Claude Code / Codex / Gemini official channels at 38% / 2% / 9% of original price, with extra discounts on top-ups! AICodeMirror offers special benefits for CLIProxyAPI users: register via <a href="https://www.aicodemirror.ai/register?invitecode=TJNAIF">this link</a> to enjoy 20% off your first top-up, and enterprise customers can get up to 25% off!</td>
</tr>
<tr>
<td width="180"><a href="https://apikey.fan/register?aff=CLIProxyAPI"><img src="./assets/apikey.png" alt="APIKEY.FUN" width="150"></a></td>
<td>Thanks to APIKEY.FUN for sponsoring this project! APIKEY.FUN is a professional enterprise-grade AI relay platform dedicated to providing stable, efficient, and low-cost AI model API access for enterprises and individual developers. The platform supports popular mainstream models such as Claude, OpenAI, and Gemini, with prices as low as 7% of the official price. Register through this project's <a href="https://apikey.fan/register?aff=CLIProxyAPI">exclusive link</a> to enjoy a special <b>permanent 5% top-up discount</b>.</td>
</tr>
<tr>
<td width="180"><a href="https://api.fenno.ai/s/Cvf0"><img src="./assets/fennoai.png" alt="FennoAI" width="150"></a></td>
<td>FennoAI is a stable and efficient API relay service provider, currently focused on Codex relay services. It is compatible with OpenAI and Anthropic protocols and can flexibly integrate with mainstream coding tools such as Codex, Claude Code, and OpenCode. It can reliably support enterprise-grade demand of hundreds of billions of tokens per day, with B2B settlement and invoicing available for both domestic and overseas entities. FennoAI offers an exclusive benefit for CLIProxyAPI users: purchase a subscription through the <a href="https://api.fenno.ai/s/Cvf0">exclusive link</a> and receive $50 worth of Coding Plan credits for just $1.99. Referral rewards are also available: earn up to 20% commission when invited friends make a purchase. The more friends you invite, the greater the rewards.</td>
</tr>
<tr>
<td width="180"><a href="https://s.qiniu.com/7zUJri"><img src="./assets/qiniucloud.png" alt="Qiniu Cloud AI" width="150"></a></td>
<td>Thanks to <a href="https://s.qiniu.com/7zUJri">Qiniu Cloud AI</a> for sponsoring this project! Qiniu Cloud AI is an enterprise-grade large-model MaaS platform under Qiniu Cloud (02567.HK). It provides one-stop access to 150+ mainstream global models, is compatible with protocols from major global model providers, and covers full-modal processing capabilities for text, image, audio, video, and files. It serves more than 1.69 million enterprise and developer users. Exclusive benefits: enterprise users can claim 12 million free tokens, and invite friends to earn up to tens of billions of tokens.</td>
</tr>
<tr>
<td width="180"><a href="https://cubence.com/signup?code=CLIPROXYAPI&source=cpa"><img src="./assets/cubence.png" alt="Cubence" width="150"></a></td>
<td>Thanks to Cubence for sponsoring this project! Cubence is a reliable and efficient API relay service provider, offering relay services for Claude Code, Codex, Gemini, and more. Cubence provides special discounts for our software users: register using <a href="https://cubence.com/signup?code=CLIPROXYAPI&source=cpa">this link</a> and enter the "CLIPROXYAPI" promo code during recharge to get 10% off.</td>
</tr>
<tr>
<td width="180"><a href="https://bestproxy.com/?keyword=ayh7otlb"><img src="./assets/bestproxy.png" alt="Bestproxy" width="150"></a></td>
<td>Bestproxy provides high-purity residential IPs with dedicated one-IP-per-account support. 🟡Residential Proxy - &#36;0.5/GB；🟡Static Residential Proxy - Starting at &#36;3/IP；🟡Unlimited Residential Proxy - Starting at &#36;67/Day.  ✅<a href="https://bestproxy.com/?keyword=ayh7otlb">Get Free Trial.</a></td>
</tr>
<tr>
<td width="180"><a href="https://go.apimart.ai/gh-cliproxyapi"><img src="./assets/apimart-en.png" alt="APIMart" width="150"></a></td>
<td>Thanks to APIMart for sponsoring this project! APIMart is a low-cost API platform for AI image &amp; video generation — GPT-Image-2 from &#36;0.006/image, 160+ images per dollar. One async API covers both image and video: submit a task, get an ID, fetch results via polling or callback. Batch tens of thousands of images without timeouts, switch models without changing code. Pay-as-you-go with no monthly fee — <a href="https://go.apimart.ai/gh-cliproxyapi">sign up here</a> to get started.</td>
</tr>
<tr>
<td width="180"><a href="https://www.swiftproxy.net/?code=PR67S9A95"><img src="./assets/swiftproxy.png" alt="Swiftproxy" width="150"></a></td>
<td>Swiftproxy provides 90M+ clean residential IPs across 220+ locations, supporting HTTP(S)/SOCKS5, IP rotation, Sticky Sessions, and precise location targeting. It helps AI API tools and automation workflows access online services reliably from different locations, making it ideal for API requests, web access, data collection, and location-based testing. Residential proxies from &#36;0.7/GB. Free testing is available, with 10% off using code PROXY90. <a href="https://www.swiftproxy.net/?code=PR67S9A95">Try Swiftproxy Now</a></td>
</tr>
<tr>
<td width="180"><a href="https://www.rapidproxy.io/?code=KHM9B6E6M"><img src="./assets/rapidproxy.png" alt="RapidProxy" width="150"></a></td>
<td><a href="https://www.rapidproxy.io/?code=KHM9B6E6M">RapidProxy</a> is a high-performance proxy provider built for automation and multi-account operations, offering clean residential proxies and native static ISP IPs. With 90M+ residential IPs worldwide, smart rotation, stable sessions, and high-concurrency support, RapidProxy is ideal for web scraping, browser automation, social media account management, e-commerce operations, bulk account registration, and more. Residential proxies start at just &#36;0.55/GB, with traffic that never expires. Use code RAPID10 for 10% off, and <a href="https://www.rapidproxy.io/?code=KHM9B6E6M">start your free trial today.</a></td>
</tr>
<tr>
<td width="180"><a href="https://pateway.ai/?ch=jjvdb"><img src="./assets/patewayai.png" alt="PatewayAI" width="150"></a></td>
<td>PatewayAI is an API relay service provider for experienced AI developers, with full support for the Claude and Codex model families. All models come from high-quality official channels, with no dilution or counterfeiting, and transparent, verifiable billing details. Economy mode starts at just 5% of the official price. Register through <a href="https://pateway.ai/?ch=jjvdb">this link</a> to receive trial credits and participate in occasional promotions for free credits. The platform also supports enterprise-grade concurrency, a dedicated management dashboard, formal contracts and invoices, and two-way referral rewards of up to &#36;150.</td>
</tr>
<tr>
<td width="180"><a href="https://www.openlux.ai/register?channel=c_6axb1q5n"><img src="./assets/openlux.png" alt="OpenLux" width="150"></a></td>
<td>Thank you to OpenLux for sponsoring this project!<br><br>OpenLux is an all-in-one AI platform for businesses, bringing together leading AI models from major providers worldwide. With fast, reliable service and responsive technical support, OpenLux offers base pricing for Claude, OpenAI, and Gemini models as low as 8.82%, 4%, and 8% of official rates, respectively.<br><br>Exclusive offer for CLI Proxy API users: Sign up through our referral link and enjoy up to 7.5% off credit top-ups!<br><br>Get started with OpenLux → <a href="https://www.openlux.ai/register?channel=c_6axb1q5n">https://www.openlux.ai/register?channel=c_6axb1q5n</a></td>
</tr>
<tr>
<td width="180"><a href="https://modelflare.dev/sign-up?utm_source=github&utm_campaign=cliproxyapi"><img src="./assets/modelflare.png" alt="Modelflare" width="150"></a></td>
<td>Thanks to Modelflare for sponsoring this project! Modelflare: Full strength, stable, nothing watered down. Global SOTA models at a lower cost. Operated by US-based Havenbyte LLC, Modelflare supports GPT, Claude, Gemini, Grok, and other leading models through common compatible protocols, plus 40+ image and video models. Cache compensation is a highlight: selected OpenAI groups (price-first, stable, and premium) guarantee daily cache hit rates of 65%, 75%, or 85%, and if eligible requests fall short, Modelflare refunds the difference, visible on the usage dashboard after settlement. Special offers: a single top-up of US&#36;20 unlocks the GPT discount group at a 0.015x price multiplier, and a single top-up of US&#36;50 unlocks the Claude discount group at a 0.03x multiplier. Register via <a href="https://modelflare.dev/sign-up?utm_source=github&utm_campaign=cliproxyapi">this exclusive link</a> to get started.</td>
</tr>
<tr>
<td width="180"><a href="https://kedaya.ai/register?aff=824X8RAHCYXM"><img src="./assets/kedaya.jpg" alt="kedaya.ai" width="150"></a></td>
<td>kedaya.ai is a stable and efficient AI model API relay service provider, covering leading models including Claude, GPT, Gemini, Grok, Antigravity, and DeepSeek. It supports native OpenAI (including Responses), Anthropic, and Gemini protocols, enabling seamless integration with clients such as CLIProxyAPI, Claude Code, Codex, Gemini CLI, and Grok CLI. OpenAI and Grok model prices can be as low as 0.1% of official rates! <a href="https://kedaya.ai/register?aff=824X8RAHCYXM">Exclusive registration link</a>.</td>
</tr>
<tr>
<td width="180"><a href="https://xcode.best/register?aff=OuDv"><img src="./assets/xcode.gif" alt="Xcode" width="150"></a></td>
<td>This AI model transfer service provider offers immediate refunds; any user can obtain a free invoice; it can be done for both corporate and individual clients; all major models are available. The customer service is always online and ready to respond to your requests; just let us know what you need and we'll take care of it！！！
<br><br>Xcode VIP access：<a href="https://xcode.best/register?aff=OuDv">https://xcode.best</a></td>
</tr>
<tr>
<td width="180"><a href="https://x.com/chenju_ai"><img src="./assets/chenju.png" alt="Chenju" width="150"></a></td>
<td>Thanks to <a href="https://x.com/chenju_ai">陈局 (Chenju)</a> for sponsoring this project! Probably the only slot here that won't ask you to sign up — no signup link, no promo code. Just raising a glass to the maintainers, because good open source deserves to be paid for. Come shoot the breeze on X: <a href="https://x.com/chenju_ai">@chenju_ai</a></td>
</tr>
</tbody>
</table>

## Overview

- OpenAI/Gemini/Claude/Grok compatible API endpoints for CLI models
- OpenAI Codex support (GPT models) via OAuth login
- Claude Code support via OAuth login
- Grok Build support via OAuth login
- Streaming, non-streaming, and WebSocket responses where supported
- Function calling/tools support
- Multimodal input support (text and images)
- Multiple accounts with round-robin load balancing (Gemini, OpenAI, Claude, Grok)
- Simple CLI authentication flows (Gemini, OpenAI, Claude, Grok)
- Generative Language API Key support
- AI Studio Build multi-account load balancing
- Claude Code multi-account load balancing
- OpenAI Codex multi-account load balancing
- Grok Build multi-account load balancing
- OpenAI-compatible upstream providers via config (e.g., OpenRouter)
- Reusable Go SDK for embedding the proxy (see `docs/sdk-usage.md`)

## Getting Started

CLIProxyAPI Guides: [https://help.router-for.me/](https://help.router-for.me/)

## Management API

see [MANAGEMENT_API.md](https://help.router-for.me/management/api)

## Fleet fork

This fork (branch `fleet`, rebased on upstream `main`) adds per-client-key policies, a management endpoint for temporary credential blocks, quota-aware routing strategies and scoped key-issuer credentials. Upstream behaviour is unchanged when none of the new configuration is present.

### Per-client-key policy

`access.api-keys` entries may be plain strings (unchanged) or objects. Patterns use `*` and are case-insensitive. Credential patterns match the auth file name in the auth dir, with or without `.json`, or the credential ID.

```yaml
access:
  api-keys:
    - key: "<macbook-key>"
      name: macbook
    - key: "<m1-key>"
      name: m1
      excluded-models: ["claude-fable-*"]
```

A request for a model outside the policy answers `403`:

```json
{"error":{"message":"model claude-fable-5-1 is not allowed for this API key","type":"permission_error","code":"model_not_allowed"}}
```

`/v1/models`, the Anthropic model listing and `/v1beta/models` only list models the key may use, and credential selection only considers credentials the key may use.

### Temporary credential block

Block one Claude credential for the `m1` key by editing `config.yaml` (the file watcher applies it without a restart):

```yaml
access:
  api-keys:
    - key: "<m1-key>"
      name: m1
      excluded-models: ["claude-fable-*"]
      excluded-credentials: ["claude-work.json"]
```

Or through the management API (same `X-Management-Key` / Bearer auth as the other management endpoints; responses never contain key material):

```sh
# read
curl -H "X-Management-Key: $MGMT" http://host:8317/v8/management/client-key-policies/m1
# block
curl -X PATCH -H "X-Management-Key: $MGMT" -H 'Content-Type: application/json' \
  -d '{"add":{"excluded-credentials":["claude-work.json"]}}' \
  http://host:8317/v8/management/client-key-policies/m1
# unblock
curl -X PATCH -H "X-Management-Key: $MGMT" -H 'Content-Type: application/json' \
  -d '{"remove":{"excluded-credentials":["claude-work.json"]}}' \
  http://host:8317/v8/management/client-key-policies/m1
```

`GET /v8/management/client-key-policies` lists every entry (by `name` or zero-based `index`), `PUT` replaces the four lists.

### Routing strategies

```yaml
routing:
  strategy: nearest-reset   # or most-used
```

- `nearest-reset`: within a provider, among credentials eligible for the request, pick the one whose rate-limit window resets soonest; credentials in cooldown or at their limit are skipped; ties go to the higher utilisation; credentials without a quota sample come after sampled ones.
- `most-used`: highest utilisation first, ties go to the soonest reset; same skipping rules.

Both read the rate-limit headers the proxy already records per credential (`x-codex-primary-*` for Codex, `anthropic-ratelimit-unified-5h-*` for Claude). Providers without such headers fall back to fill-first order.

### Use-or-lose routing

To drain weekly capacity before it expires, select:

```yaml
routing:
  strategy: use-or-lose
  session-affinity: false
```

The config watcher applies this without a restart. Roll back the routing policy by setting `routing.strategy` to `nearest-reset` (restore the previous session-affinity setting if changed). The other strategies retain their existing behaviour.

- Rank by the all-model weekly reset, or the Fable weekly reset for Fable requests when reported. A Fable weekly window can come from an earlier Fable response even if the newest response is for Opus. Codex uses its primary or secondary seven-day window, not its five-hour reset.
- A five-hour reading of at least 95% places the credential behind every eligible credential with short-window room. After that reset the cap is ignored. If every eligible credential is capped, one is still tried; quota headers alone never exclude a credential or trigger an extra upstream call.
- Among resets within one hour of the earliest reset, prefer lower utilisation; equal utilisation prefers the earlier reset, then existing input order. Anchoring the tie group to the earliest reset avoids inconsistent pairwise comparisons across three or more accounts.
- Disabled credentials, model exclusions, per-key credential policies and backend cooldowns retain their existing eligibility rules. Existing credential priorities and Codex websocket preference apply within the short-cap tier.
- Missing, invalid or expired weekly samples start behind valid samples. After one minute in the eligible ranking pool, each unsampled credential gets a normal request as a probe, oldest due first, at most once per minute per credential and model family (Fable versus other models). With N due credentials and continuing traffic, all get an attempt within the next N selections. No traffic means no probe; a response without quota headers cannot create a sample. Unsupported providers keep fill-first order.
- Session affinity, if explicitly enabled, still retains healthy bindings and applies weekly ranking/probes only when invoking the fallback selector. Keep it disabled for per-request weekly ranking and the bounded sampling guarantee. A five-hour cap can rebind an existing session.

### Key-issuer credentials

`access.key-issuer-keys` entries (same shape as `api-keys`) may only list, create and revoke client keys; every other management endpoint answers `403`.

Issuer keys are read at startup only: the config watcher does not reload `access.key-issuer-keys`, so adding, rotating or removing one needs a service restart (`systemctl --user restart cliproxyapi`). Client keys created through the issuer take effect without a restart.

```yaml
access:
  key-issuer-keys:
    - key: "<issuer-key>"
      name: orchestrator
```

```sh
# create (the client key is returned once)
curl -X POST -H "X-Management-Key: $ISSUER" -H 'Content-Type: application/json' \
  -d '{"name":"worker-01","models":["gpt-*"],"ttl":"720h"}' \
  http://host:8317/v8/management/issued-keys
# list (no key material)
curl -H "X-Management-Key: $ISSUER" http://host:8317/v8/management/issued-keys
# revoke
curl -X DELETE -H "X-Management-Key: $ISSUER" http://host:8317/v8/management/issued-keys/worker-01
```

Issued keys are stored in `access.api-keys` with `issued-by`, `created-at` and optional `expires-at`; expired keys stop authenticating.

### Credential health phone alerts

Home Assistant can turn the fork's credential-health webhook into a phone notification. Add this top-level block to `config.yaml` (also present in `config.example.yaml`):

```yaml
fleet-alerts:
  webhook-url: "" # e.g. http://<home-assistant-lan-ip>:8123/api/webhook/<id>
  stuck-after: 1h
  check-interval: 1m
```

An empty `webhook-url` disables the feature completely: no worker, hook or HTTP requests. With a URL configured, omitted durations default to `1h` and `1m`; explicit durations must be positive. These settings are read **at startup only**; restart the proxy after changing them. The worker and its auto-disable hook share a service lifetime rather than being replaced by the configuration watcher.

The worker checks every provider. Credential-wide cooldowns (including `credential_quota`), terminal authorization failures and credential-level error states count as unhealthy. A cooldown's expiry makes it healthy again unless a separate credential-level error remains. Model-only restrictions, even an aggregated error from `claude-fable-*`, do not count. Credentials manually disabled through the management API do not alert.

Events are sent once per observed transition:

- `credential_disabled`: immediately when the fork auto-disables a credential after an organization OAuth refusal; delivery runs in the background, never on the request path.
- `credential_unhealthy`: after continuously observing credential-level unavailability for `stuck-after`, including credentials already reported as auto-disabled.
- `credential_recovered`: once a previously reported credential is available again (re-enabling an auto-disabled credential also permits recovery).

Every event is an HTTP `POST` to `webhook-url` with `Content-Type: application/json` and exactly these fields:

```json
{
  "event": "credential_unhealthy",
  "provider": "claude",
  "account": "user@example.com",
  "auth_file": "claude-xxxx-user@example.com.json",
  "reason": "credential_quota",
  "status_message": "Credential quota is exhausted",
  "since": "2026-11-04T10:00:00Z",
  "retry_at": "2026-11-04T14:00:00Z",
  "proxy_version": "dev"
}
```

`event` is `credential_unhealthy`, `credential_disabled` or `credential_recovered`. `provider` is the provider identifier, `account` is the credential email or label (falling back to its auth filename), and `auth_file` is only the basename. `reason` is a short machine code such as `credential_quota`, `oauth_refused`, `payment_required`, `unauthorized` or `recovered`. `status_message` is a safe, fixed human-readable summary, never a raw upstream error, and is at most 300 characters. `since` is the RFC3339 start of the observed unhealthy episode; recovery retains that timestamp. `retry_at` is RFC3339 or an empty string. `proxy_version` is the binary's build version. Tokens, keys, cookies and raw auth content are never included.

Requests have a 10-second timeout and do not follow redirects. Non-2xx responses and transport failures log a warning without exposing the webhook URL or response body, then retry on the next check tick. Pending transitions remain ordered per credential, and a successful event is not repeated while its state holds. Delivery is at-least-once after ambiguous network failures, so the receiver should tolerate duplicates. Observation, deduplication and pending deliveries are in memory and reset on restart; an auto-disabled credential loaded from disk without the runtime auto-disable event cannot be distinguished from manual disablement and is not reported. Configure alerts before the auto-disable happens. The Home Assistant receiver and phone-notification automation are configured separately.

### Own limits

`GET /v1/fleet/limits` lets a client read its own remaining quota with its own client key (the same `Authorization: Bearer` / `x-api-key` forms `/v1/models` accepts). Management and issuer keys are refused with `401`, and so is a request without a client key even when no client keys are configured (other routes stay open in that case). The answer is built from in-memory state only (credentials, stored quota headers, cooldowns, config); it never calls an upstream.

```sh
curl -H "Authorization: Bearer $CLIENT_KEY" http://host:8317/v1/fleet/limits
```

```json
{
  "object": "fleet.limits",
  "generated_at": "2026-11-03T20:00:00Z",
  "proxy_version": "8.1.0",
  "client": {"name": "m1"},
  "models": [{"id": "claude-opus-5-5", "providers": ["claude"]}],
  "exclusions": [
    {"pattern": "claude-fable-*", "provider": "claude", "source": "oauth-excluded-models", "until": "2026-11-08T16:00:00Z", "until_source": "quota_reset"},
    {"pattern": "claude-fable-*", "provider": null, "source": "key-policy", "until": null, "until_source": null}
  ],
  "credentials": [{
    "label": "claude-6cc0eb0c", "provider": "claude", "plan": null, "state": "active",
    "bars": [
      {"id": "weekly_all", "percent": 64, "status": "allowed", "resets_at": "2026-11-08T16:00:00Z", "sampled_at": "2026-11-03T19:00:00Z", "reset_passed": false},
      {"id": "weekly_fable", "percent": 100, "status": "rejected", "resets_at": "2026-11-08T16:00:00Z", "sampled_at": "2026-11-03T18:00:00Z", "reset_passed": false}
    ],
    "cooldowns": [{"scope": "model", "model": "claude-fable-5-1", "reason": "quota", "until": "2026-11-08T16:00:30Z", "http_status": 429}]
  }],
  "summary": {"claude": {"usable_credentials": 1, "bars": {
    "weekly_all": {"percent": 64, "resets_at": "2026-11-08T16:00:00Z"},
    "weekly_fable": {"percent": 100, "resets_at": "2026-11-08T16:00:00Z"},
    "session_5h": {"percent": 0, "resets_at": null}
  }}}
}
```

- `models` is exactly what `/v1/models` lists for the key; `credentials` only lists credentials the key may use. `label` is the provider plus an opaque id (keyed by the management secret, random per process without one); it never contains an e-mail, account id or file name.
- `state` is `disabled`, `cooldown` (credential-wide cooldown in force), `error` or `active`. `cooldowns` are the timers still in force, as in management `auth-files`, minus model cooldowns of models the key may not use.
- Claude bars: `weekly_all` (`anthropic-ratelimit-unified-7d-*`), `weekly_fable` (`-7d_oi-*`, only sent with Fable responses), `session_5h` (only when the plan sends `-5h-*`; Ultra does not). Codex bars come from the primary and secondary windows (`10080` minutes is `weekly_all`, `300` is `session_5h`, others `window_<minutes>m`); Codex also gets `plan` and `credits`. Each bar uses the freshest stored response carrying it; `reset_passed` means the window rolled over since that sample.
- `summary` is per provider over usable credentials (not disabled, no credential-wide cooldown): the lowest `percent` per bar, a rolled-over bar counting as `0`. For `session_5h` only, a credential with stored quota headers but no 5h window (Ultra) counts as `0`, since its plan has no 5h cap; a credential with no stored headers yet is left out of every bar, so `weekly_all` and `weekly_fable` only come from credentials that sent that window.
- Stored quota headers live in memory only and are empty after a restart until the next upstream response; cooldowns survive a restart only with `save-cooldown-status: true`.

### Excluded models

A model matching `oauth-excluded-models` answers `403` instead of `400 unknown provider`, without `Retry-After`:

```json
{"error":{"type":"model_excluded","code":"model_excluded","param":"model","message":"model claude-fable-5-1 is excluded on this proxy until 2026-11-08T16:00:00Z (oauth-excluded-models, provider claude); pick another model","until":"2026-11-08T16:00:00Z"}}
```

`/v1/messages` gets `{"type":"error","error":{"type":"model_excluded","message":"...","until":"..."}}`. `until` is `null` (and the message says "until further notice") when no end is known. A key-policy exclusion still answers `403 model_not_allowed`.

`until` comes from, in order: the optional `oauth-excluded-models-until` config entry (informational only, never adds or lifts an exclusion; hot-reloaded; past times are ignored), the latest future reset among the rejected quota windows stored for a matching model (Claude `7d_oi`, `7d`, `5h` or unified status `rejected`; Codex primary/secondary windows at `100` percent, or every window when only the limit-reached flag is set), the latest cooldown of a matching model.

```yaml
oauth-excluded-models:
  claude: [claude-fable-5-1, claude-fable-*]
oauth-excluded-models-until:
  claude:
    claude-fable-*: "2026-11-08T16:00:00Z"
```

### Deploy

`scripts/fleet/deploy.sh <host> <linux-binary> [client-key]` copies the binary into `~/cliproxyapi/<version>/`, switches the `~/cliproxyapi/cli-proxy-api` symlink, restarts the `cliproxyapi` user unit and checks `/v1/models` with and without a key. `scripts/fleet/rollback.sh <host> [version]` switches back (default `8.0.39`).

## Usage Statistics

Since v6.10.0, CLIProxyAPI and [CPAMC](https://github.com/router-for-me/Cli-Proxy-API-Management-Center) no longer ship built-in usage statistics. If you need usage statistics, use:

### [CPA Usage Keeper](https://github.com/Willxup/cpa-usage-keeper)

Standalone persistence and visualization service for CLIProxyAPI, with periodic data sync, SQLite storage, aggregate APIs, and a built-in dashboard for usage and statistics.

### [CPA-Manager-Plus](https://github.com/seakee/CPA-Manager-Plus)

A self-hosted CLIProxyAPI management console and observability dashboard, available as a lightweight drop-in panel or a full Manager Server with persistent analytics. Manage OAuth credentials, AI providers, client API keys, model aliases, plugins, and CPA settings in one interface. SQLite-backed request history enables live monitoring, failure diagnosis, and token, latency, and estimated-cost breakdowns by model, provider, account, key, and channel. Supports editable model prices and one-click price sync, alongside Codex and xAI quota checks, reset information, account-health inspection, and controlled recovery actions. Deploy via Docker or cross-platform native packages, with data stored locally.

### [Oh-My-CPA](https://github.com/WizisCool/oh-my-cpa)

Modern Ant Design-based management console for CLIProxyAPI v8+, combining CPAMC-style administration with SQLite-backed request records and usage analytics. Covers OAuth accounts, API providers, client keys, quotas, pricing and CPA operations in one interface. Tracks request-level latency, TTFT, tokens and cost, with multi-dimensional filtering, live dashboards and token heatmaps. OpenRouter price sync, custom rates and per-request price snapshots keep historical costs stable; a built-in Agent and MCP tools support assisted investigation and management.

## SDK Docs

- Usage: [docs/sdk-usage.md](docs/sdk-usage.md)
- Advanced (executors & translators): [docs/sdk-advanced.md](docs/sdk-advanced.md)
- Access: [docs/sdk-access.md](docs/sdk-access.md)
- Watcher: [docs/sdk-watcher.md](docs/sdk-watcher.md)
- Custom Provider Example: `examples/custom-provider`

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

1. Fork the repository
2. Create your feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add some amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

## Who is with us?

Those projects are based on CLIProxyAPI:

### [vibeproxy](https://github.com/automazeio/vibeproxy)

Native macOS menu bar app to use your Claude Code & ChatGPT subscriptions with AI coding tools - no API keys needed

### [Subtitle Translator](https://github.com/VjayC/SRT-Subtitle-Translator-Validator)

A cross-platform desktop and web app to translate and validate SRT subtitles using your existing LLM subscriptions (Gemini, ChatGPT, Claude, etc.) via CLIProxyAPI - no API keys needed.

### [CCS (Claude Code Switch)](https://github.com/kaitranntt/ccs)

CLI wrapper for instant switching between multiple Claude accounts and alternative models (Gemini, Codex, Antigravity) via CLIProxyAPI OAuth - no API keys needed

### [Quotio](https://github.com/nguyenphutrong/quotio)

Native macOS menu bar app that unifies Claude, Gemini, OpenAI, and Antigravity subscriptions with real-time quota tracking and smart auto-failover for AI coding tools like Claude Code, OpenCode, and Droid - no API keys needed.

### [ProxyPilot](https://github.com/Finesssee/ProxyPilot)

Windows-native CLIProxyAPI fork with TUI, system tray, and multi-provider OAuth for AI coding tools - no API keys needed.

### [CPA-XXX Panel](https://github.com/ferretgeek/CPA-X)

A lightweight web admin panel for CLIProxyAPI with health checks, resource monitoring, real-time logs, auto-update, request statistics and pricing display. Supports one-click installation and systemd service.

### [CLIProxyAPI Tray](https://github.com/kitephp/CLIProxyAPI_Tray)

A Windows tray application implemented using PowerShell scripts, without relying on any third-party libraries. The main features include: automatic creation of shortcuts, silent running, password management, channel switching (Main / Plus), and automatic downloading and updating.

### [霖君](https://github.com/wangdabaoqq/LinJun)

霖君 is a cross-platform desktop application for managing AI programming assistants, supporting macOS, Windows, and Linux systems. Unified management of Claude Code, Gemini, OpenAI Codex, and other AI coding tools, with local proxy for multi-account quota tracking and one-click configuration.

### [CLIProxyAPI Dashboard](https://github.com/itsmylife44/cliproxyapi-dashboard)

A modern web-based management dashboard for CLIProxyAPI built with Next.js, React, and PostgreSQL. Features real-time log streaming, structured configuration editing, API key management, OAuth provider integration for Claude/Gemini/Codex, usage analytics, container management, and config sync with OpenCode via companion plugin - no manual YAML editing needed.

### [All API Hub](https://github.com/qixing-jk/all-api-hub)

Browser extension for one-stop management of New API-compatible relay site accounts, featuring balance and usage dashboards, auto check-in, one-click key export to common apps, in-page API availability testing, and channel/model sync and redirection. It integrates with CLIProxyAPI through the Management API for one-click provider import and config sync.

### [Shadow AI](https://github.com/HEUDavid/shadow-ai)

Shadow AI is an AI assistant tool designed specifically for restricted environments. It provides a stealthy operation
mode without windows or traces, and enables cross-device AI Q&A interaction and control via the local area network (
LAN). Essentially, it is an automated collaboration layer of "screen/audio capture + AI inference + low-friction delivery",
helping users to immersively use AI assistants across applications on controlled devices or in restricted environments.

### [ProxyPal](https://github.com/buddingnewinsights/proxypal)

Cross-platform desktop app (macOS, Windows, Linux) wrapping CLIProxyAPI with a native GUI. Connects Claude, ChatGPT, Gemini, GitHub Copilot, and custom OpenAI-compatible endpoints with usage analytics, request monitoring, and auto-configuration for popular coding tools - no API keys needed.

### [CLIProxyAPI Quota Inspector](https://github.com/AllenReder/CLIProxyAPI-Quota-Inspector)

Ready-to-use cross-platform quota inspector for CLIProxyAPI, supporting per-account codex 5h/7d quota windows, plan-based sorting, status coloring, and multi-account summary analytics.

### [CLIProxy Pool Watch](https://github.com/murasame612/CLIProxyPoolWidget)

Native macOS SwiftUI app for monitoring ChatGPT/Codex account quotas in CLIProxyAPI pools. Displays account availability, Plus-base capacity, 5-hour and weekly quota bars, plan weights, and restore forecasts through the Management API.

### [Panopticon](https://github.com/eltmon/panopticon-cli)

Multi-agent orchestration for AI coding assistants. Runs CLIProxyAPI as a local sidecar so its agents can drive GPT models through a ChatGPT subscription, pointing Claude Code at an Anthropic-compatible endpoint with no OpenAI API key required.

### [Tunnel Agent](https://github.com/Villoh/tunnel-agent)

Windows desktop UI that manages CLIProxyAPI and Perplexity WebUI Scraper from a single interface, inspired by Quotio and VibeProxy. Connect OAuth providers (Claude, Gemini, Codex, Kimi, Antigravity), custom API keys, and Perplexity session accounts, then point any coding agent at the local endpoint.

### [Quotio Desktop](https://github.com/xiaocoss/quotio-desktop)

Cross-platform (Tauri) port of Quotio for Windows, macOS and Linux. Manages a pool of AI accounts (Codex, Claude Code, GitHub Copilot, Gemini, Antigravity, Kiro, Cursor, Trae, GLM) through CLIProxyAPI, with per-account 5-hour/weekly quota bars, Codex rate-limit reset credits with one-click reset, smart scheduling, usage statistics, and multi-instance Codex — no API keys needed.

### [Universal Chat Provider](https://github.com/maxdewald/vscode-universal-chat-provider)

VS Code extension that brings your Claude, ChatGPT/Codex, Antigravity, Grok, and Kimi subscriptions into GitHub Copilot Chat as native language models — and can power your Git commit messages, chat titles, and summaries too. Runs CLIProxyAPI in a fully managed background lifecycle (download, verify, supervise) shared across all windows, so it's zero-setup. No API keys needed, just OAuth.

### [CPA-Tray-Powershell](https://github.com/ztzpro/CPA-Tray-Powershell.git)

A PowerShell-based Windows system tray launcher for CLIProxyAPI. It supports running in the background without a console window, opening the management page, keeping the backend running after the management window closes, and reopening the page from the tray. It also supports checking for CLIProxyAPI updates on startup, SHA-256 verification with rollback, one-click CLIProxyAPI restart and update, PID-validated process management, and safe service shutdown.

### [Grok Search MCP](https://github.com/MapleMapleCat/Grok_Search_Mcp)

An HTTP-only Model Context Protocol server that uses a CLIProxyAPI deployment to provide Grok-powered real-time web search, X/Twitter search, and model discovery to MCP clients. It adds MCP transport, client API-key management, quotas, usage tracking, and a web administration panel.

### [AIUsage](https://github.com/sylearn/AIUsage)

Native macOS SwiftUI dashboard for AI subscriptions and coding proxies. It manages official CLIProxyAPI releases end to end (download, verify, supervise, update, and roll back), unifies OAuth accounts and live models, and connects one gateway to Codex, Claude Code/Science, OpenCode, or OpenAI/Anthropic/Gemini clients, with optional LAN access.

### [Claude Dialects](https://github.com/stefandevo/claude-dialects)

Run multiple native-feeling Claude Code commands, each powered by a different model (Codex, GLM, Kimi, Gemini, Grok, MiniMax, DeepSeek, Cursor, Copilot, Claude). Every dialect launches the real Claude Code interface with its own isolated config, history, ports, and an embedded CLIProxyAPI instance linked through the Go SDK — no separate proxy install. macOS only. Learn more at [claude-dialects.cc](https://claude-dialects.cc/).

### [WebBrain](https://github.com/webbrain-one/webbrain)

Browser agent that can connect to CLIProxyAPI's local OpenAI-compatible endpoint as a model provider. See WebBrain's independent [setup, security, and account-risk guide](https://webbrain.one/docs/easy-cli-proxy/) for using CLIProxyAPI through EasyCLIProxyAPI.

### [Infinitus](https://github.com/deathemperor/infinitus)

Native macOS menu bar app that runs a fleet of Claude accounts through CLIProxyAPI's Management API (claude-swap and 9Router too): 5h / 7d / per-model quota gauges, switch / hold / star from the popup, a run-rate forecast of when each window runs out, and an iPhone companion that mirrors it all - no API keys needed.

### [PiCloud](https://github.com/cookerpapa/pi-cloud)

Self-hosted coding-agent platform built on the Pi SDK, with a web UI, concurrent subagents, and CubeSandbox KVM workspaces. Uses CLIProxyAPI as its provider gateway, keeping provider credentials outside the guest workspaces.

### [cc-status-line](https://github.com/kinka/cc-status-line)

Claude Code status line for CLIProxyAPI: per-account Codex / Grok / Antigravity / Claude quotas (5h / 7d / weekly) and reset countdown for the current instance. Picks the CPA instance from `ANTHROPIC_BASE_URL` and reads quotas through the Management API.

### [CLIProxy Quota Tray](https://github.com/ZYHUO/CLIProxy-Quota-Tray)

Cross-platform Electron tray dashboard for CLIProxyAPI. Shows the real OAuth quota windows per account across ChatGPT/Codex, Claude, Gemini/Antigravity, Grok, Kimi and Cursor, estimates usage-queue cost, and tracks OpenAI/Claude service status. Dark terminal-style UI; runs on Windows and Linux.

### [panel4cliproxyapi](https://github.com/yaanlaan/panel4cliproxyapi)

A modern, responsive web dashboard and management console for CLIProxyAPI built with React and Tailwind CSS. Features multi-provider OAuth login with LAN redirect assistance, visual zero-YAML configuration, real-time traffic analytics charts, client API key management, core version checking & hot-reloading, and a live SSE streaming playground.

### [sub2api](https://github.com/PlaygrndLabs/sub2api)

A Cloudflare-based LLM gateway and proxy server: CLIProxyAPI deployed to Cloudflare Workers + Containers in one click. Logins and config persist in R2, the dashboard sits behind a password, and API calls are refused until a client API key exists.

### [Hermes CLIProxyAPI Provider](https://github.com/duu261/hermes-cliproxyapi-provider)

Model-provider plugin for [Hermes Agent](https://github.com/NousResearch/hermes-agent) that registers one Hermes provider per CLIProxyAPI provider, each on the wire its executor accepts (Anthropic Messages, Responses or Chat Completions), with context windows and reasoning levels read from CLIProxyAPI. Optional read-only Management API pass for exact provider grouping. Listed in the [Hermes plugin catalog](https://hermes-agent.nousresearch.com/docs/plugins/cliproxyapi-provider): `hermes plugins install cliproxyapi-provider`.

> [!NOTE]  
> If you developed a project based on CLIProxyAPI, please open a PR to add it to this list.

## More choices

Those projects are ports of CLIProxyAPI or inspired by it:

### [9Router](https://github.com/decolua/9router)

A Next.js implementation inspired by CLIProxyAPI, easy to install and use, built from scratch with format translation (OpenAI/Claude/Gemini/Ollama), combo system with auto-fallback, multi-account management with exponential backoff, a Next.js web dashboard, and support for CLI tools (Cursor, Claude Code, Cline, RooCode) - no API keys needed.

### [OmniRoute](https://github.com/diegosouzapw/OmniRoute)

Never stop coding. Smart routing to FREE & low-cost AI models with automatic fallback.

OmniRoute is an AI gateway for multi-provider LLMs: an OpenAI-compatible endpoint with smart routing, load balancing, retries, and fallbacks. Add policies, rate limits, caching, and observability for reliable, cost-aware inference.

### [Codex Switch](https://github.com/9ycrooked/CodexSwitch)

This is a tool built with Tauri 2 + Vue 3 for managing multiple OpenAI Codex desktop accounts. Switch between saved ChatGPT/Codex certification profiles, check 5-hour and weekly quota usage in real time, verify token health, view active account details, and import or save auth.json files without manual copying.

### [cliproxy-rs](https://github.com/vayungodara/cliproxy-rs)

A Rust port of CLIProxyAPI that reads the same `config.yaml` and auth files and serves the same routes and v8 Management API, so you can switch between the two in either direction. Ships as a single binary with the management dashboard built in (per-account 5-hour and weekly quota view, account sign-in, client setup guides), and adds an opt-in `soonest-reset` routing strategy that spends the account whose weekly window resets first.

> [!NOTE]  
> If you have developed a port of CLIProxyAPI or a project inspired by it, please open a PR to add it to this list.

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.
