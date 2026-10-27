#!/usr/bin/env node
/**
 * chatmod 压测脚本：N 个玩家各自连续发 M 条消息，最后一条是违禁消息。
 *
 * 只依赖 Node 内置能力（Node 22+ 自带 WebSocket），不需要 npm install。
 *
 * 用法：
 *   node scripts/loadtest-chatmod.mjs
 *   node scripts/loadtest-chatmod.mjs --players=10 --messages=20 --base=http://127.0.0.1:8043
 *
 * 常用参数：
 *   --base=http://127.0.0.1:8043   服务地址（WebSocket 走 {base}/ws）
 *   --players=10                   参与压测的玩家数
 *   --messages=20                  每个玩家发多少条（最后一条是违禁消息）
 *   --prefix=                      只用名字以该前缀开头的玩家（默认取语料库里前 N 个未禁言的）
 *   --create-missing               玩家不够时自动补建（默认不建，只提示）
 *   --gap=0                        同一玩家两条消息之间的间隔毫秒（0 = 背靠背，最压）
 *   --violation-text=你是不是傻逼   违禁消息内容（需能命中现有规则，默认命中 r002）
 *   --timeout=20000                单条消息等待判定的超时毫秒
 *   --json=                        结果写入的 JSON 路径（默认 loadtest-chatmod-<时间>.json）
 *
 * 量的是两件事，别混在一起看：
 *   1. 服务端判定耗时（结果里的 entry.durationMs，纯服务端处理）
 *   2. 端到端往返（从 ws.send 到收到 result，含网络与事件队列）
 */

const args = new Map();
for (const raw of process.argv.slice(2)) {
  const m = /^--([^=]+)(?:=(.*))?$/.exec(raw);
  if (m) args.set(m[1], m[2] === undefined ? "1" : m[2]);
}
const cfg = {
  base: (args.get("base") || "http://127.0.0.1:8043").replace(/\/$/, ""),
  players: Number(args.get("players") || 10),
  messages: Number(args.get("messages") || 20),
  prefix: args.get("prefix") || "",
  createMissing: args.has("create-missing"),
  gap: Number(args.get("gap") || 0),
  violationText: args.get("violation-text") || "你是不是傻逼",
  timeout: Number(args.get("timeout") || 20000),
  json: args.get("json") || "",
};
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

if (typeof WebSocket !== "function") {
  console.error("需要 Node 22+（自带 WebSocket）。当前 node " + process.version);
  process.exit(1);
}

// ---------- WebSocket 小封装 ----------
// 服务刚启动时端口可能还没就绪，握手失败重试几次，别把机器启动慢当成压测失败。
async function connect(retries = 5) {
  let lastErr;
  for (let i = 0; i < retries; i++) {
    try {
      return await connectOnce();
    } catch (err) {
      lastErr = err;
      await sleep(300);
    }
  }
  throw lastErr;
}

function connectOnce() {
  const url = cfg.base.replace(/^http/, "ws") + "/ws";
  return new Promise((resolve, reject) => {
    const ws = new WebSocket(url);
    const waiters = [];
    let closed = false;

    ws.onmessage = (ev) => {
      let msg;
      try {
        msg = JSON.parse(ev.data);
      } catch {
        return;
      }
      for (let i = 0; i < waiters.length; i++) {
        if (waiters[i].pred(msg)) {
          const w = waiters.splice(i, 1)[0];
          clearTimeout(w.timer);
          w.resolve(msg);
          return;
        }
      }
    };
    ws.onclose = () => {
      closed = true;
      for (const w of waiters.splice(0)) {
        clearTimeout(w.timer);
        w.reject(new Error("WebSocket 已断开"));
      }
    };
    ws.onerror = () => {
      if (!closed) reject(new Error("WebSocket 连接失败: " + url));
    };
    ws.onopen = () =>
      resolve({
        send: (obj) => ws.send(JSON.stringify(obj)),
        waitFor: (pred, timeoutMs = cfg.timeout) =>
          new Promise((res, rej) => {
            if (closed) return rej(new Error("WebSocket 已断开"));
            const timer = setTimeout(() => {
              const i = waiters.findIndex((w) => w.timer === timer);
              if (i >= 0) waiters.splice(i, 1);
              rej(new Error("等待事件超时（" + timeoutMs + "ms）"));
            }, timeoutMs);
            waiters.push({ pred, resolve: res, reject: rej, timer });
          }),
        close: () => ws.close(),
      });
  });
}

// ---------- 统计 ----------
function percentile(values, p) {
  if (!values.length) return 0;
  const sorted = [...values].sort((a, b) => a - b);
  const idx = Math.min(sorted.length - 1, Math.max(0, Math.ceil((p / 100) * sorted.length) - 1));
  return sorted[idx];
}
function stats(values) {
  const v = values.filter((x) => Number.isFinite(x));
  if (!v.length) return { count: 0, avg: 0, p50: 0, p95: 0, p99: 0, max: 0 };
  const sum = v.reduce((a, b) => a + b, 0);
  return {
    count: v.length,
    avg: +(sum / v.length).toFixed(2),
    p50: +percentile(v, 50).toFixed(2),
    p95: +percentile(v, 95).toFixed(2),
    p99: +percentile(v, 99).toFixed(2),
    max: +Math.max(...v).toFixed(2),
  };
}
const ms = (x) => (x >= 100 ? x.toFixed(0) : x.toFixed(1));

// ---------- 主流程 ----------
async function main() {
  console.log(`chatmod 压测 → ${cfg.base}   玩家 ${cfg.players} × 消息 ${cfg.messages}（每玩家最后 1 条为违禁）`);

  // 1) 先摸一次语料库与玩家列表
  const probe = await connect();
  const boot = await probe.waitFor((m) => m.type === "bootstrap");
  let players = boot.players || [];
  if (cfg.prefix) players = players.filter((p) => (p.name || "").startsWith(cfg.prefix));
  const usable = players.filter((p) => p.id && !p.mute);
  if (usable.length < cfg.players) {
    if (!cfg.createMissing) {
      probe.close();
      console.error(
        `可用玩家只有 ${usable.length} 个（需要 ${cfg.players} 个，已排除禁言中的）。\n` +
          `请先在页面上建好玩家，或加 --create-missing 让脚本自动补建。`
      );
      process.exit(2);
    }
    for (let i = usable.length; i < cfg.players; i++) {
      probe.send({ type: "player.create", name: `${cfg.prefix || "压测"}${String(i + 1).padStart(2, "0")}` });
      await probe.waitFor((m) => m.type === "ack" || m.type === "error");
    }
    const boot2 = await (async () => {
      probe.send({ type: "noop" }); // noop 会被忽略，这里靠重新连一次拿最新列表
      probe.close();
      const again = await connect();
      const b = await again.waitFor((m) => m.type === "bootstrap");
      again.close();
      return b;
    })();
    players = (boot2.players || []).filter((p) => (!cfg.prefix ? true : (p.name || "").startsWith(cfg.prefix)));
    const refill = players.filter((p) => p.id && !p.mute);
    if (refill.length < cfg.players) {
      console.error(`补建后可用玩家仍只有 ${refill.length} 个，无法继续。`);
      process.exit(2);
    }
    console.log(`已补建玩家，当前可用 ${refill.length} 个`);
  } else {
    probe.close();
  }
  const chosen = players.filter((p) => p.id && !p.mute).slice(0, cfg.players);
  console.log(`玩家：${chosen.map((p) => `${p.id}:${p.name}`).join("、")}`);

  // 2) 每个玩家一条独立 WebSocket（模拟 10 个真实用户并发，而不是一个人连点）
  const startedAt = Date.now();
  const perPlayer = await Promise.all(
    chosen.map(async (player, pi) => {
      const c = await connect();
      await c.waitFor((m) => m.type === "bootstrap");
      c.send({ type: "set_player", playerId: player.id });
      await c.waitFor((m) => m.type === "player_selected");

      const rows = [];
      const t0 = Date.now();
      for (let i = 1; i <= cfg.messages; i++) {
        const isViolation = i === cfg.messages;
        const text = isViolation
          ? cfg.violationText
          : `大家好，这是第 ${String(i).padStart(2, "0")} 条测试消息（玩家${pi + 1}号）`;
        const sendAt = performance.now();
        c.send({ type: "send", text });
        let msg;
        try {
          msg = await c.waitFor((m) => m.type === "result" || m.type === "error");
        } catch (err) {
          rows.push({ i, text, expectedViolation: isViolation, error: err.message, rttMs: performance.now() - sendAt });
          break;
        }
        const rttMs = performance.now() - sendAt;
        if (msg.type === "error") {
          rows.push({ i, text, expectedViolation: isViolation, error: msg.message, rttMs });
        } else {
          const r = msg.result || {};
          const entry = r.entry || {};
          rows.push({
            i,
            text,
            expectedViolation: isViolation,
            rttMs,
            // 注意：entry.durationMs 是禁言时长，不是处理耗时；服务端处理耗时看 result.judgeMs。
            serverMs: r.judgeMs,
            seq: entry.seq,
            rejected: !!r.rejected,
            rejectReason: r.rejectReason || "",
            severity: entry.severity || 0,
            isViolation: !!entry.isViolation,
            hits: (entry.hits || []).map((h) => h.ruleId),
          });
        }
        if (cfg.gap > 0 && i < cfg.messages) await sleep(cfg.gap);
      }
      c.close();
      return { id: player.id, name: player.name, wallMs: Date.now() - t0, rows };
    })
  );
  const wallMs = Date.now() - startedAt;

  // 3) 汇总
  const all = perPlayer.flatMap((p) => p.rows);
  const sent = all.length;
  const errors = all.filter((r) => r.error);
  const rejected = all.filter((r) => r.rejected);
  const accepted = all.filter((r) => !r.error && !r.rejected);
  const violations = all.filter((r) => r.expectedViolation);
  const detected = violations.filter((r) => r.isViolation);

  const result = {
    project: "chatmod",
    base: cfg.base,
    config: {
      players: cfg.players,
      messagesPerPlayer: cfg.messages,
      gapMs: cfg.gap,
      violationText: cfg.violationText,
      mode: "WebSocket，服务端同步判定（规则引擎，无模型调用）",
    },
    at: new Date().toISOString(),
    totals: {
      messages: sent,
      accepted: accepted.length,
      rejected: rejected.length,
      errors: errors.length,
      violationsSent: violations.length,
      violationsDetected: detected.length,
    },
    wallMs,
    throughputMsgPerSec: +(sent / (wallMs / 1000)).toFixed(1),
    latency: {
      serverProcessingMs: stats(all.map((r) => r.serverMs)),
      clientRoundTripMs: stats(all.map((r) => r.rttMs)),
    },
    perPlayer: perPlayer.map((p) => ({
      id: p.id,
      name: p.name,
      messages: p.rows.length,
      wallMs: p.wallMs,
      avgRttMs: +(p.rows.reduce((a, r) => a + (r.rttMs || 0), 0) / p.rows.length).toFixed(2),
    })),
  };

  const L = (label, s) =>
    `${label}  p50 ${ms(s.p50)}ms  p95 ${ms(s.p95)}ms  p99 ${ms(s.p99)}ms  max ${ms(s.max)}ms  avg ${ms(s.avg)}ms`;
  console.log("");
  console.log("结果");
  console.log(`  消息 ${result.totals.messages} 条 · 接受 ${result.totals.accepted} · 拒绝 ${result.totals.rejected} · 错误 ${result.totals.errors}`);
  console.log(`  违禁消息命中 ${result.totals.violationsDetected}/${result.totals.violationsSent}`);
  console.log(`  总耗时 ${(wallMs / 1000).toFixed(3)}s · 吞吐 ${result.throughputMsgPerSec} 条/秒`);
  console.log("  " + L("服务端判定", result.latency.serverProcessingMs));
  console.log("  " + L("端到端往返", result.latency.clientRoundTripMs));
  if (result.totals.rejected > 0) {
    console.log(`  拒绝原因样例：${[...new Set(rejected.map((r) => r.rejectReason).filter(Boolean))].join("；")}`);
  }
  if (result.totals.violationsDetected < result.totals.violationsSent) {
    console.log(`  ⚠ 有 ${result.totals.violationsSent - result.totals.violationsDetected} 条违禁消息没被判定为违规：检查语料库里有没有能命中「${cfg.violationText}」的规则。`);
  }
  if (errors.length) console.log(`  ⚠ 错误样例：${errors[0].error}`);

  const out = cfg.json || `loadtest-chatmod-${Date.now()}.json`;
  const { writeFileSync } = await import("node:fs");
  writeFileSync(out, JSON.stringify({ ...result, rows: all }, null, 2));
  console.log(`  明细已写入 ${out}`);
}

main().catch((err) => {
  console.error("压测失败：" + (err && err.message ? err.message : err));
  process.exit(1);
});
