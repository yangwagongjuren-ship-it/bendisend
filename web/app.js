"use strict";

const $ = (id) => document.getElementById(id);
const composer = $("composer");
const textInput = $("text-input");
const timeline = $("timeline");
const chunkSize = 8 * 1024 * 1024;
const gib = 1024 ** 3;
const encoder = new TextEncoder();
let shortTime = new Intl.DateTimeFormat("zh-CN", { hour: "2-digit", minute: "2-digit", hour12: false });
let shortDate = new Intl.DateTimeFormat("zh-CN", { year: "numeric", month: "long", day: "numeric" });
let numberFormat = new Intl.NumberFormat("zh-CN", { maximumFractionDigits: 2 });

let state = null;
let invitation = null;
let etag = "";
let shownMessages = "";
let refreshTask = null;
let opening = false;
let toastTimer;
let composing = false;

let files = [];
let pending = null;
let requestedDraft = null;
let sendTask = null;
let chunkRequest = null;
let canceling = false;
let autoResume = false;
let selectedIndex = null;
let quotaProblem = null;
let continueAfterLimits = false;

class APIError extends Error {
  constructor(status, problem = {}) {
    const errorKeys = {
      room_deleted: "roomDeleted", file_limit: "fileLimit", room_limit: "totalLimit",
      disk_space: "diskSpace", offset_conflict: "uploadProgressChanged", draft_busy: "deviceDraftBusy",
    };
    const message = currentLanguage() === "en"
      ? t(errorKeys[problem.code] || "apiRequestFailed")
      : (problem.message || t("hostRequestFailed"));
    super(message);
    this.status = status;
    this.code = problem.code;
    this.limit = problem.limit;
    this.required = problem.required;
  }
}

function setConnection(status, label) {
  $("connection").dataset.state = status;
  $("connection-label").textContent = t(label);
}

function showJoin(title, message, retry = false) {
  $("join-heading").textContent = t(title);
  $("join-message").textContent = t(message);
  $("retry-join").hidden = !retry;
  $("room-view").hidden = true;
  $("join-view").hidden = false;
}

function notify(message) {
  const toast = $("toast");
  toast.textContent = t(message);
  toast.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { toast.hidden = true; }, 3700);
}

async function api(path, { method = "GET", body, headers = {}, timeout = 6000, unscoped = false } = {}) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeout);
  try {
    const generation = !unscoped && state?.room?.generation;
    if (generation) headers = { ...headers, "X-Room-Generation": generation };
    const response = await fetch(unscoped ? path : roomAPI(path), {
      method, cache: "no-store", signal: controller.signal,
      headers: body === undefined ? headers : { ...headers, "Content-Type": "application/json" },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    if (!response.ok && response.status !== 304) {
      throw new APIError(response.status, await response.json().catch(() => ({})));
    }
    const value = response.status === 204 || response.status === 304 ? null : await response.json();
    return { value, etag: response.headers.get("ETag") || "" };
  } finally {
    clearTimeout(timer);
  }
}

async function openRoom() {
  if (opening) return;
  opening = true;
  setConnection("waiting", "正在连接");
  showJoin("正在打开房间", "请稍候。");
  const token = location.hash.slice(1);
  try {
    if (/^[A-Za-z0-9_-]{43}$/.test(token)) {
      await api("/api/join", { method: "POST", body: { token } });
      history.replaceState(null, "", location.pathname + location.search);
    }
    await api("/api/device", { method: "POST" });
    etag = "";
    await readTimeline();
    await loadRooms();
    $("join-view").hidden = true;
    $("room-view").hidden = false;
    scrollToLatest();
    setConnection("connected", "主机已连接");
    loadInvitation();
    if (!pending && !requestedDraft && !files.length && !textInput.value) {
      await restoreDraft();
    }
  } catch (error) {
    if (error.code === "room_deleted") {
      scheduleRoomRecovery();
    } else if (!$("room-view").hidden) {
      notify("暂时无法检查待续传消息，请刷新后重试。");
    } else if (error.status === 401 || error.status === 403) {
      setConnection("waiting", "未加入");
      showJoin("加入共享收件箱", "请扫描主机上的二维码或打开邀请链接。");
    } else {
      setConnection("offline", "无法连接主机");
      showJoin("暂时无法打开房间", "请确认主机程序正在运行，并连到同一个 Wi‑Fi。", true);
    }
  } finally {
    opening = false;
    renderRoomSelect();
    updateComposer();
  }
}

async function readTimeline() {
  const headers = etag ? { "If-None-Match": etag } : {};
  const result = await api("/api/messages", { headers });
  if (!result.value) return;
  state = result.value;
  etag = result.etag;
  $("edit-limits").hidden = !state.canEditLimits;
  renderRoomInfo(state.room, state.canEditLimits);
  renderDevices();
  renderMessages();
  updateComposer();
}

async function refresh() {
  if ($("room-view").hidden || switchingRoom || opening) return;
  if (refreshTask) return refreshTask;
  refreshTask = readTimeline().then(() => {
    setConnection("connected", "主机已连接");
    if (autoResume && !sendTask && !canceling && draftReady()) {
      autoResume = false;
      send();
    }
  }).catch((error) => {
    if (error.code === "room_deleted") {
      scheduleRoomRecovery();
    } else if (error.status === 401) {
      setConnection("waiting", "未加入");
      showJoin("请重新加入房间", "重新扫码或打开邀请链接后可继续使用。", true);
    } else {
      setConnection("offline", "正在重连");
    }
  }).finally(() => { refreshTask = null; });
  return refreshTask;
}

function element(tag, className, text) {
  const el = document.createElement(tag);
  if (className) el.className = className;
  if (text !== undefined) el.textContent = text;
  return el;
}

function button(label, className, action) {
  const el = element("button", className, t(label));
  el.type = "button";
  el.addEventListener("click", action);
  return el;
}

function bytesLabel(bytes) {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 ** 2) return `${numberFormat.format(bytes / 1024)} KiB`;
  if (bytes < gib) return `${numberFormat.format(bytes / 1024 ** 2)} MiB`;
  return `${numberFormat.format(bytes / gib)} GiB`;
}

function deviceIcon(kind) {
  const icon = element("span", "device-icon");
  icon.dataset.kind = ["iphone", "ipad", "android"].includes(kind) ? "mobile" : kind;
  icon.setAttribute("aria-hidden", "true");
  return icon;
}

function renderDevices() {
  const devices = [state.self, ...state.devices.filter(({ id }) => id !== state.self.id)];
  $("device-list").replaceChildren(...devices.map((device, index) => {
    const row = element("li", index === 0 ? "self" : "");
    row.append(deviceIcon(device.kind), element("span", "device-name", device.name));
    if (index === 0) row.append(element("span", "self-tag", "本机"));
    return row;
  }));
}

function attachmentBadge(name) {
  const ext = name.split(".").pop();
  const type = ext !== name && /^[a-z\d]{1,4}$/i.test(ext) ? ext.toUpperCase() : t("attachment");
  return element("span", "file-badge", type);
}

function renderMessages() {
  const next = `${state.self.id}:${state.messages.map(({ id }) => id).join(":")}`;
  if (next === shownMessages && $("messages").hidden === (state.messages.length === 0)) return;
  shownMessages = next;
  const nearEnd = timeline.scrollHeight - timeline.scrollTop - timeline.clientHeight < 70;
  let previousDate;
  const content = document.createDocumentFragment();
  for (const message of state.messages) {
    const date = new Date(message.createdAt);
    const day = shortDate.format(date);
    if (day !== previousDate) {
      const today = day === shortDate.format(new Date());
      content.append(element("li", "day", today ? t("today") : day));
      previousDate = day;
    }
    const self = message.sender.id === state.self.id;
    const row = element("li", self ? "message self" : "message");
    row.dataset.messageId = message.id;
    const main = element("div", "message-content");
    const heading = element("div", "message-heading");
    heading.append(element("span", "message-author", self ? `${message.sender.name} · ${t("self")}` : message.sender.name));
    const time = element("time", "message-time", shortTime.format(date));
    time.dateTime = message.createdAt;
    heading.append(time, button(t("delete"), "plain message-delete", () => removeMessage(message.id)));
    main.append(heading);
    for (const [index, file] of (message.files || []).entries()) {
      const card = element("div", "message-card file-card");
      const data = element("div", "message-data");
      data.append(element("strong", "", file.name), element("span", "muted", bytesLabel(file.size)));
      const link = element("a", "button secondary card-action", t("downloaded"));
      link.href = roomAPI(`/api/messages/${message.id}/files/${index}`);
      link.download = file.name;
      link.setAttribute("aria-label", t("downloadFile", {name: file.name}));
      card.append(attachmentBadge(file.name), data, link);
      main.append(card);
    }
    if (message.text) {
      const card = element("div", "message-card text-card");
      card.append(element("span", "text-badge", t("text")), element("p", "message-data message-text", message.text));
      card.append(button(t("copy"), "button secondary card-action", () => copyValue(message.text, t("copied"))));
      main.append(card);
    }
    row.append(deviceIcon(message.sender.kind), main);
    content.append(row);
  }
  $("messages").replaceChildren(content);
  $("messages").hidden = state.messages.length === 0;
  $("empty-state").hidden = state.messages.length > 0;
  if (nearEnd) scrollToLatest();
}

function scrollToLatest() {
  timeline.scrollTop = timeline.scrollHeight;
}

async function removeMessage(id) {
  if (!window.confirm(t("removeMessageConfirm"))) return;
  try {
    await api(`/api/messages/${id}`, { method: "DELETE" });
    await refresh();
  } catch (error) {
    notify(error instanceof APIError ? t(error.message) : t("deleteFailed"));
  }
}

async function loadInvitation() {
  const scope = activeRoom;
  try {
    const { value } = await api("/api/invites");
    if (scope !== activeRoom) return;
    invitation = value.find(({ ip }) => ip === location.hostname) || value[0];
    $("invite-content").hidden = !invitation;
    $("invite-message").textContent = invitation ? "" : t("hostNoAddress");
    if (invitation) $("qr").src = roomAPI(`/api/qr?ip=${encodeURIComponent(invitation.ip)}`);
  } catch {
    if (scope === activeRoom) $("invite-message").textContent = t("inviteReadFailed");
  }
}

function toggleDevices(open) {
  $("room-view").classList.toggle("devices-open", open);
  $("toggle-devices").setAttribute("aria-expanded", String(open));
  if (open) {
    $("invite-heading").focus();
    loadInvitation();
  }
}

async function copyValue(value, success) {
  if (navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(value);
      notify(success);
      return;
    } catch { /* Fall back to selecting the content on LAN HTTP. */ }
  }
  const field = $("copy-value");
  const dialog = $("copy-dialog");
  dialog.showModal();
  field.value = value;
  field.focus();
  field.select();
  field.setSelectionRange(0, field.value.length);
  try {
    if (document.execCommand("copy")) {
      dialog.close();
      notify(success);
    }
  } catch { /* Keep the selection available for manual copying. */ }
}

function setNotice(message = "", problem = null) {
  quotaProblem = problem;
  $("notice-text").textContent = t(message);
  $("composer-notice").hidden = !message;
  $("notice-limits").hidden = !problem || !state?.canEditLimits;
}

function newID() {
  const value = crypto.getRandomValues(new Uint8Array(16));
  return Array.from(value, (byte) => byte.toString(16).padStart(2, "0")).join("");
}

function usableName(name) {
  return name.replace(/[\x00-\x1f\x7f-\x9f/\\:<>"|?*]/g, "_").trim().replace(/^[. ]+|[. ]+$/g, "");
}

function makeFile(file) {
  return { name: usableName(file.name), size: file.size, file, verify: false };
}

function draftReady() {
  if (!state || encoder.encode(textInput.value).length > state.maxTextBytes) return false;
  if (!files.length && !textInput.value.trim()) return false;
  return !pending || files.every((file, index) => file.file || pending.offsets[index] === file.size);
}

function updateComposer() {
  const busy = Boolean(sendTask) || canceling || switchingRoom || opening;
  textInput.readOnly = busy || Boolean(requestedDraft);
  $("choose-files").disabled = busy || Boolean(requestedDraft && !pending)
    || Boolean(pending && files.every((file, index) => file.file || pending.offsets[index] === file.size));
  $("choose-label").textContent = pending ? t("chooseOriginal") : t("chooseFile");
  $("send").disabled = busy || !draftReady();
  $("send").textContent = busy ? t("sending") : requestedDraft ? t("continueSending") : t("send");
  $("cancel-upload").disabled = canceling || switchingRoom;
  $("transfer-progress").hidden = !requestedDraft && !sendTask;
}

function renderDraft() {
  $("attachment-field").hidden = !files.length;
  $("attachment-list").replaceChildren(...files.map((file, index) => {
    const item = element("li", "draft-file");
    const info = element("div", "draft-file-info");
    const needsSource = pending && !file.file && pending.offsets[index] < file.size;
    info.append(element("strong", "", file.name), element("span", "muted", needsSource ? `${bytesLabel(file.size)} · ${t("reselect")}` : bytesLabel(file.size)));
    item.append(attachmentBadge(file.name), info);
    if (!requestedDraft) {
      const remove = button("×", "plain", () => {
        files.splice(index, 1);
        setNotice();
        renderDraft();
      });
      remove.setAttribute("aria-label", t("removeAttachment", {name:file.name}));
      item.append(remove);
    } else if (pending && (needsSource || file.verify) && !sendTask) {
      const reselect = button(t("reselect"), "plain", () => chooseFiles(index));
      reselect.setAttribute("aria-label", t("reselectFile", {name:file.name}));
      item.append(reselect);
    }
    return item;
  }));
  updateComposer();
}

function chooseFiles(index = null) {
  selectedIndex = index;
  $("file-input").multiple = index === null;
  $("file-input").click();
}

function stageFiles(selection, target = null) {
  if (!state || sendTask || canceling || switchingRoom || opening || (requestedDraft && !pending) || !selection.length) return;
  if (!pending) {
    const next = Array.from(selection, makeFile);
    if (files.length + next.length > state.maxFiles) {
      setNotice(t("maxFiles", {count:state.maxFiles}));
      return;
    }
    files.push(...next);
    setNotice();
  } else {
    let matched = 0;
    for (const file of selection) {
      const index = target ?? files.findIndex((entry, i) => !entry.file && pending.offsets[i] < entry.size && entry.name === usableName(file.name) && entry.size === file.size);
      if (index >= 0 && files[index].name === usableName(file.name) && files[index].size === file.size) {
        files[index].file = file;
        files[index].verify = true;
        matched++;
      }
    }
    setNotice(matched === selection.length ? "" : t("resumeFileMismatch"));
  }
  renderDraft();
}

function progress(label, current = 0, total = 0) {
  $("progress-label").textContent = t(label);
  $("progress-bar").value = total ? current / total * 100 : 0;
}

async function restoreDraft() {
  const { value } = await api("/api/transfers/active");
  if (!value || files.length || textInput.value) return;
  pending = value;
  requestedDraft = { id: value.id, text: value.text, files: value.files || [] };
  files = requestedDraft.files.map((file) => ({ ...file, file: null, verify: true }));
  textInput.value = value.text || "";
  setNotice(draftReady() ? t("pendingAvailable") : t("pendingNeedsFiles"));
  updateProgress();
  renderDraft();
}

function updateProgress(extra = 0) {
  const total = files.reduce((sum, file) => sum + file.size, 0);
  const received = (pending?.offsets || []).reduce((sum, value) => sum + value, 0);
  progress(total ? t("uploadedProgress", {current:bytesLabel(received+extra), total:bytesLabel(total)}) : t("submittingMessage"), received + extra, total);
}

async function digest(blob) {
  try {
    const data = await blob.arrayBuffer();
    const hash = crypto.subtle
      ? new Uint8Array(await crypto.subtle.digest("SHA-256", data))
      : (await import("/hash/sha2.js")).sha256(new Uint8Array(data));
    return Array.from(hash, (byte) => byte.toString(16).padStart(2, "0")).join("");
  } catch {
    throw new Error(t("verifyFileFailed"));
  }
}

async function verifySource(id, index, file, offset) {
  if (!file.verify || !offset || !file.file) return;
  const parts = Math.ceil(offset / chunkSize);
  for (let part = 0; part < parts;) {
    if (canceling || switchingRoom) return;
    const { value: hashes } = await api(`/api/transfers/${id}/files/${index}/checksums?from=${part}`);
    if (!hashes.length) throw new Error(t("uploadProgressChanged"));
    for (const expected of hashes) {
      if (part >= parts || canceling || switchingRoom) break;
      const start = part * chunkSize;
      const actual = await digest(file.file.slice(start, Math.min(start + chunkSize, file.size)));
      if (actual !== expected) {
        file.file = null;
        throw new Error(t("fileContentMismatch", {name:file.name}));
      }
      part++;
      progress(t("checkingFile", {name:file.name}), part, parts);
    }
  }
  if (!canceling && !switchingRoom) file.verify = false;
}

function putChunk(id, index, offset, data) {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    chunkRequest = xhr;
    xhr.open("PUT", roomAPI(`/api/transfers/${id}/files/${index}`));
    xhr.timeout = 120000;
    xhr.setRequestHeader("Content-Type", "application/octet-stream");
    xhr.setRequestHeader("Upload-Offset", String(offset));
    if (state?.room?.generation) xhr.setRequestHeader("X-Room-Generation", state.room.generation);
    xhr.upload.onprogress = (event) => updateProgress(event.loaded);
    xhr.onload = () => {
      let value;
      try { value = JSON.parse(xhr.responseText); } catch { value = {}; }
      if (xhr.status === 200 && Number.isSafeInteger(value.offset)) resolve(value.offset);
      else reject(new APIError(xhr.status, value));
    };
    const disconnect = () => reject(new TypeError(t("disconnectedPreserved")));
    xhr.onerror = disconnect;
    xhr.ontimeout = disconnect;
    xhr.onabort = disconnect;
    xhr.send(data);
  }).finally(() => { chunkRequest = null; });
}

async function putWithRetry(id, index, offset, data) {
  for (let attempt = 0; ; attempt++) {
    if (canceling || switchingRoom) return offset;
    try {
      return await putChunk(id, index, offset, data);
    } catch (error) {
      if (canceling || attempt === 2 || !(error instanceof TypeError || error.status === 429)) throw error;
      progress(t("retryingUpload"));
      await new Promise((resolve) => setTimeout(resolve, 700 * 2 ** attempt));
    }
  }
}

async function finishSend() {
  files = [];
  textInput.value = "";
  pending = null;
  requestedDraft = null;
  autoResume = false;
  setNotice();
  if (refreshTask) await refreshTask;
  await refresh();
  scrollToLatest();
  notify("已发送");
}

function showSendProblem(error, wasRegistered) {
  if (error.code === "room_deleted") {
    scheduleRoomRecovery();
    return;
  }
  if (error.code === "file_limit" || error.code === "room_limit") {
    const name = error.code === "file_limit" ? t("fileLimit") : t("totalLimit");
    const next = state.canEditLimits ? "" : t("hostEditQuota");
    setNotice(t("quotaMessage", {name, limit:bytesLabel(error.limit), required:bytesLabel(error.required), next}), error);
    requestedDraft = null;
  } else if (error.code === "disk_space" || error.status === 507 || error.code === "draft_busy") {
    if (!wasRegistered) requestedDraft = null;
    setNotice(error.message);
  } else if (error.status === 404 && wasRegistered) {
    pending = null;
    requestedDraft = null;
    files = files.filter((file) => file.file);
    setNotice(t("pendingExpired"));
  } else if (error instanceof APIError) {
    if (!wasRegistered && error.status < 500) requestedDraft = null;
    setNotice(error.message);
  } else if (error instanceof TypeError || error.name === "AbortError") {
    autoResume = true;
    setNotice(t("reconnectToResume"));
  } else {
    setNotice(t(error.message || "暂时无法继续上传。"));
  }
}

async function runSend() {
  setNotice();
  progress(t("checkingQuota"));
  if (!requestedDraft) {
    requestedDraft = { id: newID(), text: textInput.value, files: files.map(({ name, size }) => ({ name, size })) };
  }
  const id = requestedDraft.id;
  try {
    const result = pending
      ? await api(`/api/transfers/${id}`)
      : await api("/api/transfers", { method: "POST", body: requestedDraft });
    if (result.value.published) return await finishSend();
    pending = result.value;
    if (!draftReady()) {
      updateProgress();
      setNotice(t("hostRestored"));
      return;
    }
    for (const [index, file] of files.entries()) {
      if (canceling || switchingRoom) return;
      await verifySource(id, index, file, pending.offsets[index]);
      while (!canceling && !switchingRoom && pending.offsets[index] < file.size) {
        const offset = pending.offsets[index];
        const data = file.file.slice(offset, Math.min(offset + chunkSize, file.size));
        pending.offsets[index] = await putWithRetry(id, index, offset, data);
        updateProgress();
      }
    }
    if (canceling || switchingRoom) return;
    progress(t("submitting"));
    await api(`/api/transfers/${id}/commit`, { method: "POST", timeout: 12000 });
    await finishSend();
  } catch (error) {
    if (!canceling && !switchingRoom) showSendProblem(error, Boolean(pending));
  }
}

function send() {
  if (sendTask || canceling || switchingRoom || opening || !draftReady()) return;
  autoResume = false;
  sendTask = runSend().finally(() => {
    sendTask = null;
    renderDraft();
  });
  renderDraft();
}

async function cancelUpload() {
  if (!requestedDraft || canceling || switchingRoom) return;
  canceling = true;
  autoResume = false;
  const id = requestedDraft.id;
  updateComposer();
  chunkRequest?.abort();
  try {
    await sendTask;
    if (requestedDraft) {
      await api(`/api/transfers/${id}`, { method: "DELETE" });
      requestedDraft = null;
      pending = null;
      files = files.filter((file) => file.file);
      setNotice(t("uploadCanceled"));
    }
  } catch (error) {
    setNotice(error instanceof APIError ? t(error.message) : t("cancelRequestFailed"));
  } finally {
    canceling = false;
    renderDraft();
  }
}

function limitError(message = "") {
  $("limits-error").textContent = message;
  $("limits-error").hidden = !message;
}

async function openLimits(fromNotice = false) {
  if (!state?.canEditLimits || $("limits-dialog").open) return;
  const needed = fromNotice ? quotaProblem : null;
  continueAfterLimits = Boolean(needed);
  limitError();
  $("limits-usage").textContent = t("readingHostSpace");
  $("save-limits").disabled = true;
  $("save-limits").textContent = needed ? t("saveAndContinue") : t("save");
  $("limits-dialog").showModal();
  try {
    const { value } = await api("/api/limits");
    if (!$("limits-dialog").open) return;
    const file = needed?.code === "file_limit" ? Math.max(value.maxFileBytes, needed.required) : value.maxFileBytes;
    const room = Math.max(value.maxRoomBytes, file, needed?.code === "room_limit" ? needed.required : 0);
    $("max-file").value = file / gib;
    $("max-room").value = room / gib;
    $("limits-usage").textContent = t("hostUsage", {used:bytesLabel(value.usedBytes), pending:bytesLabel(value.pendingBytes), free:bytesLabel(value.freeBytes)});
    $("save-limits").disabled = false;
    $("max-file").focus();
  } catch (error) {
    $("limits-usage").textContent = "";
    limitError(error instanceof APIError ? t(error.message) : t("hostSpaceReadFailed"));
  }
}

async function saveLimits(event) {
  event.preventDefault();
  const maxFileBytes = Math.round($("max-file").valueAsNumber * gib);
  const maxRoomBytes = Math.round($("max-room").valueAsNumber * gib);
  if (!Number.isSafeInteger(maxFileBytes) || !Number.isSafeInteger(maxRoomBytes) || maxFileBytes <= 0 || maxFileBytes > maxRoomBytes) {
    limitError(t("limitsInvalid"));
    return;
  }
  $("save-limits").disabled = true;
  limitError();
  try {
    await api("/api/limits", { method: "PUT", body: { maxFileBytes, maxRoomBytes } });
    $("limits-dialog").close();
    if (continueAfterLimits) send();
    else notify(t("limitsSaved"));
    refresh();
  } catch (error) {
    limitError(error instanceof APIError ? t(error.message) : t("limitsSaveFailed"));
    $("save-limits").disabled = false;
  }
}

$("retry-join").addEventListener("click", openRoom);
$("toggle-devices").addEventListener("click", () => toggleDevices($("toggle-devices").getAttribute("aria-expanded") !== "true"));
$("close-devices").addEventListener("click", () => { toggleDevices(false); $("toggle-devices").focus(); });
$("edit-limits").addEventListener("click", () => openLimits());
$("notice-limits").addEventListener("click", () => openLimits(true));
$("limits-form").addEventListener("submit", saveLimits);
$("close-limits").addEventListener("click", () => $("limits-dialog").close());
$("qr").addEventListener("error", () => { $("invite-message").textContent = t("qrFailed"); });
$("copy-invite").addEventListener("click", () => { if (invitation) copyValue(invitation.url, t("inviteCopied")); });
$("choose-files").addEventListener("click", () => chooseFiles());
$("file-input").addEventListener("change", (event) => {
  stageFiles(event.target.files, selectedIndex);
  event.target.value = "";
  selectedIndex = null;
});
$("cancel-upload").addEventListener("click", cancelUpload);
composer.addEventListener("submit", (event) => { event.preventDefault(); send(); });
textInput.addEventListener("input", () => {
  if (!state) return;
  if (encoder.encode(textInput.value).length > state.maxTextBytes) setNotice(t("textTooLong", {size:bytesLabel(state.maxTextBytes)}));
  else setNotice();
  updateComposer();
});
textInput.addEventListener("compositionstart", () => { composing = true; });
textInput.addEventListener("compositionend", () => { composing = false; });
window.addEventListener("bendisend:languagechange", () => {
  const locale = currentLanguage();
  shortTime = new Intl.DateTimeFormat(locale, { hour: "2-digit", minute: "2-digit", hour12: false });
  shortDate = new Intl.DateTimeFormat(locale, { year: "numeric", month: "long", day: "numeric" });
  numberFormat = new Intl.NumberFormat(locale, { maximumFractionDigits: 2 });
  shownMessages = "";
  if (state) { renderRoomInfo(state.room, state.canEditLimits); renderDevices(); renderMessages(); }
  renderRoomSelect();
  renderDraft();
  if (requestedDraft) updateProgress();
  if ($("limits-dialog").open) $("save-limits").textContent = continueAfterLimits ? t("saveAndContinue") : t("save");
  notify(t("languageChanged"));
});
textInput.addEventListener("keydown", (event) => {
  if (event.key === "Enter" && !event.shiftKey && !event.isComposing && !composing && event.keyCode !== 229) {
    event.preventDefault();
    send();
  }
});
document.addEventListener("dragover", (event) => {
  if (Array.from(event.dataTransfer?.types || []).includes("Files")) event.preventDefault();
});
document.addEventListener("drop", (event) => { if (event.dataTransfer?.files.length) event.preventDefault(); });
composer.addEventListener("dragover", (event) => {
  if (!sendTask && Array.from(event.dataTransfer.types).includes("Files")) {
    event.preventDefault();
    composer.classList.add("drag-over");
  }
});
composer.addEventListener("dragleave", (event) => {
  if (!composer.contains(event.relatedTarget)) composer.classList.remove("drag-over");
});
composer.addEventListener("drop", (event) => {
  if (!event.dataTransfer.files.length) return;
  event.preventDefault();
  composer.classList.remove("drag-over");
  stageFiles(event.dataTransfer.files);
});
document.addEventListener("visibilitychange", () => { if (!document.hidden) refresh(); });
window.addEventListener("online", refresh);
window.addEventListener("pageshow", refresh);
setInterval(() => { if (!document.hidden) refresh(); }, 3000);
openRoom();
