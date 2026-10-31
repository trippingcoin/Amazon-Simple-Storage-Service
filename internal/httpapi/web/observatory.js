(() => {
  "use strict";
  const $ = (id) => document.getElementById(id),
    NS = "http://www.w3.org/2000/svg";
  const reduced = matchMedia("(prefers-reduced-motion: reduce)").matches;
  let token = "",
    paused = false,
    timer = null,
    controller = null,
    report = null,
    lastOK = 0,
    selected = "",
    paths = new Map(),
    particles = [],
    pending = [],
    eventRows = [],
    seen = new Map(),
    generation = 0;
  const fmt = (n) =>
    Number(n).toLocaleString("ru-RU", { maximumFractionDigits: 1 });
  const bytes = (n) => {
    if (n === 0) return "0 B";
    const units = ["B", "KB", "MB", "GB", "TB"];
    const i = Math.min(
      4,
      Math.floor(Math.log(Math.max(n, 1)) / Math.log(1024)),
    );
    return fmt(n / 1024 ** i) + " " + units[i];
  };
  const active = (n) => n.state === "healthy" || n.state === "degraded";
  const explanations = {
    client:
      "Клиенты используют API сервера. Счётчики включают завершённые операции; имена бакетов, ключи и тела запросов не собираются.",
    gateway:
      "Точка входа, через которую открыта панель. Сам Nginx не отдаёт телеметрию этой панели: его health checks, исключение upstream и latency отдельно не измерены.",
    database:
      "PostgreSQL хранит метаданные, права и квоты. Доступность проверяется SQL-запросом к writer. Это проверка зависимости, а не полная трассировка транзакций.",
    storage:
      "S3/MinIO хранит immutable blobs. Health — фактический HeadBucket с таймаутом 1.5 s. Объём — логические опубликованные байты в metadata, без оценки физической ёмкости.",
    backup:
      "Источник телеметрии backup не подключён. Эта панель не создаёт и не проверяет резервные копии.",
  };
  function status(text, kind = "") {
    $("connection").textContent = text;
    $("connection").className = "connection " + kind;
  }
  function notice(text, error = false) {
    $("notice").textContent = text;
    $("notice").className = "notice" + (error ? " error" : "");
  }
  function componentState(id, state, text) {
    const el = $(id);
    el.className =
      "component " +
      (id === "database"
        ? "metadata "
        : id === "storage"
          ? "object-store "
          : id === "backup"
            ? "backup "
            : "") +
      state;
    el.querySelector(".state").textContent = text;
  }
  function draw() {
    const box = $("topology").getBoundingClientRect();
    $("edges").setAttribute("viewBox", `0 0 ${box.width} ${box.height}`);
    $("wires").replaceChildren();
    paths.clear();
    const pairs = [["client", "gateway", ""]];
    for (const el of $("nodes").querySelectorAll(".component")) {
      pairs.push(
        ["gateway", el.id, ""],
        [el.id, "database", "metadata"],
        [el.id, "storage", ""],
      );
    }
    for (const [a, b, type] of pairs) {
      if (!$(a) || !$(b) || $(a).hidden || $(b).hidden) continue;
      const A = $(a).getBoundingClientRect(),
        B = $(b).getBoundingClientRect();
      const x = A.x + A.width / 2 - box.x,
        y = A.y + A.height / 2 - box.y,
        X = B.x + B.width / 2 - box.x,
        Y = B.y + B.height / 2 - box.y;
      const p = document.createElementNS(NS, "path");
      p.setAttribute(
        "d",
        `M ${x} ${y} C ${(x + X) / 2} ${y}, ${(x + X) / 2} ${Y}, ${X} ${Y}`,
      );
      p.setAttribute("class", type);
      $("wires").append(p);
      paths.set(a + "|" + b, p);
    }
  }
  function packet(a, b, color, delay = 0) {
    if (reduced || paused) return;
    if (delay) {
      if (pending.length < 80)
        pending.push({ at: performance.now() + delay, a, b, color });
      return;
    }
    if (particles.length >= 80) return;
    const reverse = !paths.has(a + "|" + b),
      path = paths.get(a + "|" + b) || paths.get(b + "|" + a);
    if (!path) return;
    const c = document.createElementNS(NS, "circle");
    c.setAttribute("r", "3");
    c.setAttribute(
      "fill",
      getComputedStyle(document.documentElement)
        .getPropertyValue("--" + color)
        .trim(),
    );
    $("particles").append(c);
    particles.push({ a, b, c, reverse, start: performance.now() });
  }
  function animate(e, id) {
    const node = "node-" + id;
    packet("client", "gateway", "blue");
    packet("gateway", node, "blue", 350);
    if (e.status !== 401 && e.status !== 403) {
      packet(node, "database", "purple", 700);
      packet("database", node, "purple", 1050);
    }
    if (["upload", "download"].includes(e.operation) && e.outcome === "success")
      packet(
        e.operation === "upload" ? node : "storage",
        e.operation === "upload" ? "storage" : node,
        "blue",
        1400,
      );
    packet(node, "gateway", e.outcome === "success" ? "blue" : "red", 1850);
    packet("gateway", "client", e.outcome === "success" ? "blue" : "red", 2200);
  }
  function frame(now) {
    const due = pending.filter((p) => p.at <= now);
    pending = pending.filter((p) => p.at > now);
    due.forEach((p) => packet(p.a, p.b, p.color));
    particles = particles.filter((p) => {
      const path = paths.get(p.a + "|" + p.b) || paths.get(p.b + "|" + p.a),
        f = (now - p.start) / 800;
      if (!path || f >= 1 || paused || !token || Date.now() - lastOK > 15000) {
        p.c.remove();
        return false;
      }
      const v = path.getPointAtLength(
        path.getTotalLength() * (p.reverse ? 1 - f : f),
      );
      p.c.setAttribute("cx", v.x);
      p.c.setAttribute("cy", v.y);
      return true;
    });
    requestAnimationFrame(frame);
  }
  function details() {
    if (!selected) return;
    const n = report?.nodes.find((n) => n.id === selected);
    $("detail").textContent = n
      ? `API ${n.id.slice(0, 8)} · ${n.state} · ${fmt(n.rps)} RPS · ${fmt(n.latency_ms)} ms mean · ${n.inflight} активных запросов · ${n.connections} TCP · ${n.requests} завершено · ${n.errors} ошибок · heartbeat ${fmt(n.age_seconds)} s назад. При перезапуске ID меняется.`
      : explanations[selected] || "Нет данных";
  }
  function events(nodes) {
    const fresh = [];
    for (const n of nodes) {
      const previous = seen.get(n.id),
        items = n.events || [];
      for (const e of items) {
        if (previous === undefined || e.sequence > previous) {
          fresh.push({ ...e, node: n.id });
          if (previous !== undefined && active(n) && fresh.length <= 12)
            animate(e, n.id);
        }
      }
      seen.set(n.id, Math.max(previous || 0, ...items.map((e) => e.sequence)));
    }
    const ids = new Set(nodes.map((n) => n.id));
    for (const id of seen.keys()) if (!ids.has(id)) seen.delete(id);
    eventRows = [...fresh, ...eventRows]
      .sort((a, b) => Date.parse(b.at) - Date.parse(a.at))
      .slice(0, 12);
    if (!eventRows.length) return;
    $("events").replaceChildren();
    for (const e of eventRows) {
      const row = document.createElement("tr");
      const values = [
        new Date(e.at).toLocaleTimeString("ru-RU"),
        e.node.slice(0, 8) + " / " + e.operation,
        String(e.status) + " · " + e.outcome,
        fmt(e.duration_ms),
      ];
      values.forEach((v, i) => {
        const td = document.createElement("td");
        td.textContent = v;
        if (i === 2) td.className = e.outcome === "success" ? "good" : "bad";
        row.append(td);
      });
      $("events").append(row);
    }
  }
  function render(r) {
    report = r;
    const live = r.nodes.filter(active),
      sum = (k) => live.reduce((a, n) => a + Number(n[k] || 0), 0),
      rps = sum("rps");
    $("rps").textContent = fmt(rps);
    $("latency").textContent =
      (rps
        ? fmt(live.reduce((s, n) => s + n.latency_ms * n.rps, 0) / rps)
        : "0") + " ms";
    $("connections").textContent = fmt(sum("connections"));
    $("inflight").textContent = fmt(sum("inflight")) + " запросов in flight";
    $("usage").textContent = r.storage.available ? bytes(r.storage.bytes) : "—";
    $("objects").textContent = r.storage.available
      ? fmt(r.storage.objects) +
        " objects · " +
        fmt(r.storage.buckets) +
        " buckets"
      : "Данные хранилища недоступны";
    $("replicas").textContent = live.length + " / " + r.nodes.length;
    $("heartbeat").textContent = r.registry
      ? "Общий реестр · 15 s stale"
      : "Только текущий процесс";
    $("mode").textContent =
      r.mode === "cluster"
        ? "Cluster · API + PostgreSQL + S3"
        : "Local · API + filesystem";
    $("database").hidden = r.mode !== "cluster";
    $("storage").querySelector("strong").textContent =
      r.mode === "cluster" ? "S3 / MinIO" : "Local filesystem";
    $("storage").querySelector("small").textContent =
      r.mode === "cluster" ? "Immutable blobs" : "Файлы + durable metadata";
    $("client").querySelector(".state").textContent = fmt(rps) + " RPS";
    $("gateway").querySelector("small").textContent =
      r.mode === "cluster" ? "Балансировщик / direct" : "Прямой HTTP-доступ";
    componentState(
      "database",
      r.database.state,
      r.database.state + " · " + fmt(r.database.latency_ms) + " ms",
    );
    componentState(
      "storage",
      r.object_store.state,
      r.object_store.state + " · " + fmt(r.object_store.latency_ms) + " ms",
    );
    const visible = r.nodes.slice(0, 12),
      signature = visible.map((n) => n.id).join(",");
    if ($("nodes").dataset.signature !== signature) {
      $("nodes").replaceChildren();
      $("nodes").dataset.signature = signature;
      for (const n of visible) {
        const btn = document.createElement("button");
        btn.type = "button";
        btn.id = "node-" + n.id;
        btn.dataset.key = n.id;
        btn.className = "component";
        const type = document.createElement("span");
        type.className = "component-type";
        type.textContent = "API REPLICA";
        const title = document.createElement("strong");
        title.textContent = n.id.slice(0, 8);
        const state = document.createElement("span");
        state.className = "state";
        const load = document.createElement("small");
        load.className = "load";
        const bar = document.createElement("div");
        bar.className = "mini-load";
        bar.append(document.createElement("i"));
        btn.append(type, title, state, bar, load);
        $("nodes").append(btn);
      }
    }
    for (const n of visible) {
      const el = $("node-" + n.id);
      el.className = "component " + n.state;
      el.querySelector(".state").textContent =
        n.state + (n.state === "stale" ? " · no heartbeat" : "");
      el.querySelector(".load").textContent = active(n)
        ? fmt(n.rps) + " RPS · " + n.inflight + " active"
        : "Последний heartbeat " + fmt(n.age_seconds) + " s";
      el.querySelector("i").style.width =
        (active(n) ? Math.min(100, (n.rps / Math.max(rps, 1)) * 100) : 0) + "%";
      el.title = `Реплика приложения обслуживает HTTP-запросы. API ${n.id} · ${n.state} · ${fmt(n.rps)} RPS · ${n.connections} TCP`;
    }
    for (const [id, key] of [
      ["pending", "pending"],
      ["retired", "retired"],
      ["deleting", "deleting"],
    ]) {
      $(id).textContent = r.storage.available ? fmt(r.storage[key]) : "—";
      $(id).classList.toggle(
        "waiting",
        r.storage.available && r.storage[key] > 0,
      );
    }
    for (const id of ["client", "gateway", "database", "storage", "backup"]) {
      $(id).title =
        explanations[id] +
        " Состояние: " +
        $(id).querySelector(".state").textContent;
    }
    $("replication").textContent =
      r.mode === "cluster" ? "S3 managed · нет данных" : "Не применяется";
    $("updated").textContent =
      "Snapshot " +
      new Date(r.sampled_at).toLocaleTimeString("ru-RU") +
      " · poll 2 s";
    const messages = {
      registry_migration_required:
        "Для всех реплик выполните: triple-s-cluster enable-observatory. Пока показан только текущий процесс.",
      registry_unavailable:
        "Реестр реплик недоступен. Показан текущий процесс; данные о других репликах отсутствуют.",
      database_unavailable: "PostgreSQL недоступен для проверки writer.",
      local_storage_unavailable: "Локальное хранилище не прошло проверку.",
      sampling_failed:
        "Сбор состояния завершился ошибкой. Показатели зависимостей неизвестны.",
      sample_stale:
        "Snapshot устарел. Состояние зависимостей и реплик неизвестно.",
    };
    notice(
      messages[r.warning] ||
        "Данные работающего сервера. Потоки — иллюстрация завершённых HTTP-операций, без трассировки внутренних шагов.",
      Boolean(r.warning),
    );
    draw();
    events(r.nodes);
    details();
  }
  function markStale() {
    pending = [];
    for (const id of [
      "rps",
      "latency",
      "connections",
      "replicas",
      "usage",
      "pending",
      "retired",
      "deleting",
    ])
      $(id).textContent = "—";
    $("inflight").textContent = "Нет свежих данных";
    $("objects").textContent = "Состояние хранилища неизвестно";
    for (const id of ["database", "storage"])
      componentState(id, "unknown", "Нет свежих данных");
    for (const el of $("nodes").querySelectorAll(".component")) {
      el.className = "component unknown";
      el.querySelector(".state").textContent = "Нет свежих данных";
    }
  }
  async function poll() {
    if (!token || paused || document.hidden || controller) return;
    const mine = generation,
      abort = new AbortController();
    controller = abort;
    const timeout = setTimeout(() => abort.abort(), 5000);
    try {
      const res = await fetch("/-/observatory/state", {
        headers: { Authorization: "Bearer " + token },
        signal: abort.signal,
        cache: "no-store",
        credentials: "omit",
        redirect: "error",
      });
      if (mine !== generation) return;
      if (res.status === 401 || res.status === 403) {
        stop();
        notice(
          res.status === 403
            ? "Нужен действующий токен администратора."
            : "Токен недействителен или отозван.",
          true,
        );
        return;
      }
      if (!res.ok) throw Error("HTTP " + res.status);
      const data = await res.json();
      if (mine !== generation) return;
      if (data.version !== 1 || !Array.isArray(data.nodes))
        throw Error("Формат snapshot не поддерживается");
      lastOK = Date.now();
      status(
        "Live · " + (data.mode === "cluster" ? "cluster" : "local"),
        "live",
      );
      render(data);
      if (["sample_stale", "sampling_failed"].includes(data.warning))
        markStale();
      $("login").hidden = true;
      $("session").hidden = false;
      $("pause").disabled = false;
    } catch (e) {
      if (mine !== generation) return;
      status("Нет связи", "error");
      notice(
        "Обновление недоступно. Последние показания устарели; проверьте сервер и авторизацию.",
        true,
      );
      if (Date.now() - lastOK > 15000) markStale();
    } finally {
      clearTimeout(timeout);
      if (mine === generation) {
        controller = null;
        timer = setTimeout(poll, 2000);
      }
    }
  }
  function stop() {
    generation++;
    token = "";
    clearTimeout(timer);
    controller?.abort();
    controller = null;
    pending = [];
    particles.forEach((p) => p.c.remove());
    particles = [];
    eventRows = [];
    seen.clear();
    report = null;
    lastOK = 0;
    selected = "";
    $("login").hidden = false;
    $("session").hidden = true;
    $("pause").disabled = true;
    $("pause").textContent = "Пауза";
    paused = false;
    $("token").value = "";
    status("Не подключено");
    for (const id of [
      "rps",
      "latency",
      "connections",
      "usage",
      "replicas",
      "pending",
      "retired",
      "deleting",
    ])
      $(id).textContent = "—";
    $("events").replaceChildren();
    $("nodes").replaceChildren();
    $("nodes").dataset.signature = "";
    $("detail").textContent = "Подключитесь, чтобы увидеть состояние сервера.";
    markStale();
    $("updated").textContent = "Данные ещё не получены";
    draw();
  }
  // Read before clearing the password field; credentials never enter URL or persistent storage.
  $("login").addEventListener("submit", (e) => {
    e.preventDefault();
    const entered = $("token").value.trim();
    stop();
    token = entered;
    if (token) {
      generation++;
      notice("Подключение…");
      poll();
    }
  });
  $("disconnect").addEventListener("click", () => {
    stop();
    notice("Сеанс отключён. Токен удалён из памяти вкладки.");
  });
  $("pause").addEventListener("click", () => {
    paused = !paused;
    generation++;
    clearTimeout(timer);
    controller?.abort();
    controller = null;
    $("pause").textContent = paused ? "Продолжить" : "Пауза";
    if (paused) {
      pending = [];
      status("Обновление на паузе");
    } else poll();
  });
  $("topology").addEventListener("click", (e) => {
    const el = e.target.closest("[data-key]");
    if (el) {
      selected = el.dataset.key;
      details();
    }
  });
  document.addEventListener("visibilitychange", () => {
    clearTimeout(timer);
    if (!document.hidden && token && !paused) poll();
  });
  new ResizeObserver(draw).observe($("topology"));
  requestAnimationFrame(frame);
})();
