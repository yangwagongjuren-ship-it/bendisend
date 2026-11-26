"use strict";

const defaultRoom = "default";
let activeRoom = /^\/r\/([a-f0-9]{32})\/$/.exec(location.pathname)?.[1] || defaultRoom;
let switchingRoom = false;
let recoveringRoom = false;
let availableRooms = [];
let mayCreateRoom = false;
let roomsRequest = 0;
const roomDrafts = new Map();
let managementBusy = false;
let failedClear = [];

function roomPage(id = activeRoom) {
  return id === defaultRoom ? "/" : `/r/${id}/`;
}

function roomAPI(path) {
  return roomPage().slice(0, -1) + path;
}

function retentionLabel(days) {
  return days === 0 ? t("retentionForever") : t("retentionDays", {days});
}

function renderRoomInfo(room, canManage) {
  $("room-retention-label").textContent = retentionLabel(room.retentionDays);
  $("manage-room").hidden = !canManage;
}

function renderRoomSelect() {
  const control = $("room-select");
  control.replaceChildren(...availableRooms.map((room) =>
    new Option(room.id === defaultRoom ? t("defaultRoom") : room.name, room.id)));
  if (mayCreateRoom) control.add(new Option(`＋ ${t("addRoom")}`, "add"));
  control.value = activeRoom;
  control.disabled = opening || switchingRoom || !availableRooms.length;
}

async function loadRooms() {
	const request = ++roomsRequest;
  const { value } = await api("/api/rooms", { unscoped: true });
  if (request !== roomsRequest) return;
  availableRooms = value.rooms;
  mayCreateRoom = value.canCreate;
  renderRoomSelect();
}

function refreshRoomsOnFocus() {
  if (state && !opening && !switchingRoom) loadRooms().catch(() => {});
}

async function switchRoom(id, { discard = false } = {}) {
  if ((!discard && id === activeRoom) || opening || switchingRoom || (canceling && !discard)) {
    renderRoomSelect();
    return;
  }
  switchingRoom = true;
  renderRoomSelect();
  updateComposer();
  if (discard) chunkRequest?.abort();
  else if (sendTask) notify(t("switchingRoom"));
  try {
    await sendTask;
    await refreshTask;
    if (discard) roomDrafts.delete(activeRoom);
    else roomDrafts.set(activeRoom, { generation: state?.room?.generation, text: textInput.value,
      files, pending, requestedDraft, notice: $("notice-text").textContent, quotaProblem });
    if (id) activeRoom = id;
    history.replaceState(null, "", roomPage());
    const saved = roomDrafts.get(id);
    const draft = saved?.generation === availableRooms.find((room) => room.id === id)?.generation ? saved : null;
    files = draft?.files || [];
    pending = draft?.pending || null;
    requestedDraft = draft?.requestedDraft || null;
    textInput.value = draft?.text || "";
    autoResume = false;
    selectedIndex = null;
    setNotice(pending ? t("uploadPaused") : draft?.notice, draft?.quotaProblem);
    if (requestedDraft) updateProgress();
    etag = "";
    state = null;
    shownMessages = "";
    invitation = null;
    $("invite-content").hidden = true;
    $("invite-message").textContent = "";
    $("qr").removeAttribute("src");
    toggleDevices(false);
    if (id) await openRoom();
    else {
      setConnection("waiting", t("unjoined"));
      showJoin(t("roomDeleted"), t("roomDeletedPrompt"));
    }
  } finally {
    switchingRoom = false;
    renderRoomSelect();
    renderDraft();
  }
}

function scheduleRoomRecovery() {
  const id = activeRoom;
  const generation = state?.room?.generation;
  setTimeout(() => {
    if (activeRoom === id && state?.room?.generation === generation) recoverDeletedRoom();
  }, 0);
}

async function recoverDeletedRoom(preferDefault = false) {
  if (recoveringRoom || opening || switchingRoom) return;
  recoveringRoom = true;
  const deletedID = activeRoom;
  const deletedGeneration = state?.room?.generation;
  try {
    try {
      await loadRooms();
    } catch (error) {
      if (!preferDefault && error.status !== 401) throw error;
      availableRooms = [];
      mayCreateRoom = false;
    }
    const next = preferDefault ? defaultRoom : availableRooms.find((room) =>
      room.id !== deletedID || (deletedGeneration && room.generation !== deletedGeneration))?.id;
    $("manage-room-dialog").close();
    $("limits-dialog").close();
    await switchRoom(next ?? null, { discard: true });
    if (next) notify(t("roomRemovedToast"));
  } catch (error) {
    notify(roomErrorMessage(error));
  } finally {
    recoveringRoom = false;
  }
}

function roomErrorMessage(error) {
  return error instanceof APIError ? t(error.message) : t("roomUnavailable");
}

async function createRoom(event) {
  event.preventDefault();
  const submit = $("create-room");
  const error = $("add-room-error");
  submit.disabled = true;
  error.hidden = true;
  try {
    const name = $("new-room-name").value.trim();
    const { value } = await api("/api/rooms", { method: "POST", unscoped: true, body: { name } });
    $("add-room-dialog").close();
    await switchRoom(value.id);
  } catch (failure) {
    error.textContent = roomErrorMessage(failure);
    error.hidden = false;
  } finally {
    submit.disabled = false;
  }
}

function managementResult(message = "") {
  const result = $("room-management-result");
  result.textContent = t(message);
  result.hidden = !message;
}

function lockManagement(busy) {
  managementBusy = busy;
  for (const id of ["save-retention", "clear-room", "delete-room", "retry-clear", "close-manage-room", "retention-days"]) {
    $(id).disabled = busy;
  }
}

async function deleteRoom() {
  if (managementBusy || !state?.canEditLimits) return;
  const room = state.room;
  const replacement = room.id === defaultRoom ? t("defaultReplacement") : "";
  if (!window.confirm(t("deleteRoomConfirm", {name: room.name, replacement}))) return;
  lockManagement(true);
  managementResult();
  try {
    await api(`/api/rooms/${room.id}`, {
      method: "DELETE", unscoped: true, body: { generation: room.generation }, timeout: 15000,
    });
    await recoverDeletedRoom(true);
  } catch (error) {
    if (error.code === "room_deleted") scheduleRoomRecovery();
    else managementResult(roomErrorMessage(error));
  } finally {
    lockManagement(false);
  }
}

async function saveRetention(event) {
  event.preventDefault();
  const days = Number($("retention-days").value);
  const current = state.room.retentionDays;
  if (days === current || managementBusy) return;
  lockManagement(true);
  managementResult();
  try {
    const { value: preview } = await api(`/api/room/retention?days=${days}`);
    if (days !== 0 && (current === 0 || days < current) &&
        !window.confirm(t("shortenRetentionConfirm", {name: state.room.name, days, count: preview.expired}))) return;
    await api("/api/room/retention", {
      method: "PUT", body: { retentionDays: days, expectedExpired: preview.expired },
    });
    if (refreshTask) await refreshTask;
    await refresh();
    managementResult(t("retentionSaved"));
  } catch (error) {
    managementResult(roomErrorMessage(error));
  } finally {
    lockManagement(false);
  }
}

async function clearRoom(retry) {
  if (managementBusy) return;
  lockManagement(true);
  managementResult();
  try {
    let ids = failedClear;
    if (!retry) {
      const { value } = await api("/api/room/clear");
      ids = value.ids;
      if (!ids.length) {
        failedClear = [];
        managementResult(t("noRoomRecords"));
        return;
      }
      if (!window.confirm(t("clearRoomConfirm", {name: value.room.name, count: ids.length}))) return;
    }
    const { value } = await api("/api/room/clear", { method: "POST", body: { ids }, timeout: 15000 });
    failedClear = value.failed;
    const message = failedClear.length
      ? `${t("clearDone", {count: value.deleted})} ${t("clearRetry", {count: failedClear.length})}`
      : t("clearDone", {count: value.deleted});
    managementResult(message);
    if (refreshTask) await refreshTask;
    await refresh();
  } catch (error) {
    managementResult(roomErrorMessage(error));
  } finally {
    $("retry-clear").hidden = !failedClear.length;
    lockManagement(false);
  }
}

document.addEventListener("DOMContentLoaded", () => {
  window.addEventListener("bendisend:languagechange", () => {
    renderRoomSelect();
    if ($("manage-room-dialog").open && state) {
      $("manage-room-heading").textContent = t("roomAdminTitle", {name: state.room.name});
    }
  });
  window.addEventListener("focus", refreshRoomsOnFocus);
  window.addEventListener("hashchange", () => {
    if (!switchingRoom && /^[A-Za-z0-9_-]{43}$/.test(location.hash.slice(1))) openRoom();
  });
  $("room-select").addEventListener("change", (event) => {
    if (event.target.value !== "add") return switchRoom(event.target.value);
    renderRoomSelect();
    $("new-room-name").value = "";
    $("add-room-error").hidden = true;
    $("add-room-dialog").showModal();
    $("new-room-name").focus();
  });
  $("add-room-form").addEventListener("submit", createRoom);
  $("close-add-room").addEventListener("click", () => $("add-room-dialog").close());
  $("manage-room").addEventListener("click", () => {
    if (!state?.canEditLimits || switchingRoom) return;
    $("manage-room-heading").textContent = t("roomAdminTitle", {name: state.room.name});
    $("retention-days").value = state.room.retentionDays;
    failedClear = [];
    $("retry-clear").hidden = true;
    managementResult();
    $("manage-room-dialog").showModal();
  });
  $("manage-room-dialog").addEventListener("cancel", (event) => {
    if (managementBusy) event.preventDefault();
  });
  $("retention-form").addEventListener("submit", saveRetention);
  $("clear-room").addEventListener("click", () => clearRoom(false));
  $("delete-room").addEventListener("click", deleteRoom);
  $("retry-clear").addEventListener("click", () => clearRoom(true));
  $("close-manage-room").addEventListener("click", () => $("manage-room-dialog").close());
});
