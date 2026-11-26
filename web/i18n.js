"use strict";

(() => {
  const messages = {
    "zh-CN": {
      product: "本地传 · BendiSend", switchRoom: "切换房间", language: "语言", defaultRoom: "默认房间",
      qrAlt: "加入房间的二维码", downloadFile: "下载 {name}",
      quotaMessage: "{name}为 {limit}，本次需要 {required}。{next}",
      diskSpace: "主机磁盘空间不足，请释放空间后重试。", messages: "传输消息",
      connecting: "正在连接", connected: "主机已连接", unjoined: "未加入", offline: "无法连接主机", reconnecting: "正在重连",
      openingRoom: "正在打开房间", pleaseWait: "请稍候。", retry: "重新连接", joinInbox: "加入共享收件箱",
      joinPrompt: "请扫描主机上的二维码或打开邀请链接。", openFailed: "暂时无法打开房间",
      openFailedPrompt: "请确认主机程序正在运行，并连到同一个 Wi‑Fi。", rejoin: "请重新加入房间",
      rejoinPrompt: "重新扫码或打开邀请链接后可继续使用。", roomDeleted: "房间已删除",
      roomDeletedPrompt: "请使用新的邀请链接或二维码加入房间。", connectDevices: "连接设备", close: "关闭",
      sameWifi: "连到同一个 Wi‑Fi", scanPhone: "手机扫码", or: "或", copyInvite: "复制邀请链接，打开即用",
      hostNoAddress: "主机暂时没有可用的局域网地址。", inviteReadFailed: "暂时无法读取邀请链接。",
      devices: "设备", self: "本机", inbox: "共享收件箱", retentionDays: "记录保留 {days} 天",
      retentionForever: "记录长期保留", manageRoom: "房间管理", transferLimits: "传输上限", timeline: "消息时间线",
      noMessages: "还没有消息", sendBelow: "在下方发送文字或附件。", editMessage: "编辑待发消息",
      pendingAttachments: "待发送附件", text: "文字", inputPlaceholder: "粘贴文字，或拖入文件…",
      uploadProgress: "上传进度", cancelUpload: "取消上传", changeLimits: "修改上限", chooseFile: "选择文件",
      chooseOriginal: "选择原文件", dragFiles: "也可拖入文件", enterHint: "Enter 发送 · Shift+Enter 换行",
      send: "发送", sending: "发送中…", continueSending: "继续发送", addRoom: "添加房间", roomName: "房间名称",
      roomExample: "例如：工作资料", cancel: "取消", createRoom: "创建房间", retentionTime: "记录保留时间",
      oneDay: "1 天", sevenDays: "7 天", thirtyDays: "30 天", longTerm: "长期保留", save: "保存",
      retryClear: "重试未清理记录", clearRoom: "清空本房间", deleteRoom: "删除房间", limitsTitle: "传输上限",
      singleFileLimit: "单文件上限（GiB）", sharedQuota: "所有房间共享总额（GiB）", manualCopy: "手动复制",
      manualCopyPrompt: "长按或使用系统快捷键复制已选内容。", downloaded: "下载", delete: "删除", copy: "复制",
      copied: "文字已复制", today: "今天", attachment: "附件", removeAttachment: "移除附件 {name}", reselect: "重选",
      reselectFile: "重新选择 {name}", fileRequiresReselect: "需重选", maxFiles: "每条消息最多附带 {count} 个文件。",
      resumeFileMismatch: "请选择名称和大小与待续传附件一致的原文件。", pendingAvailable: "找到一条待续传消息，可以继续发送。",
      pendingNeedsFiles: "找到一条待续传消息，请选择原文件继续。", uploadedProgress: "已上传 {current} / {total}",
      submittingMessage: "正在提交消息", checkingFile: "正在核对「{name}」", verifyFileFailed: "暂时无法校验原文件。请重选文件或刷新页面后重试。",
      uploadProgressChanged: "主机上的进度已变化，请重新连接后继续。", fileContentMismatch: "「{name}」内容与已上传部分不同，请重选原文件。",
      disconnectedPreserved: "连接中断，已确认的部分会保留。", retryingUpload: "连接中断或主机繁忙，正在重试…",
      sent: "已发送", fileLimit: "单文件上限", totalLimit: "所有房间共享总额",
      hostEditQuota: "请在 Windows 主机上修改，再回来继续发送。", pendingExpired: "待续传消息已过期。可以重新选择附件发送。",
      reconnectToResume: "连接中断，草稿和已确认的分片会保留。联网后继续发送。", resumeFailed: "暂时无法继续上传。",
      checkingQuota: "正在检查传输额度…", hostRestored: "主机已恢复，请选择缺少的原文件继续。",
      submitting: "正在提交消息…", uploadCanceled: "已取消上传，仍可编辑待发内容。",
      cancelRequestFailed: "未能通知主机取消，请连接后重试。", readingHostSpace: "正在读取主机空间…",
      saveAndContinue: "保存并继续发送", hostUsage: "已用 {used} · 上传预留 {pending} · 磁盘可用 {free}",
      hostSpaceReadFailed: "无法读取主机空间，请重试。", limitsInvalid: "请输入大于零的有效容量，共享总额须大于或等于单文件上限。",
      limitsSaved: "传输上限已保存", limitsSaveFailed: "暂时无法保存，请重试。", qrFailed: "二维码暂时无法加载，可以复制邀请链接。",
      inviteCopied: "邀请链接已复制", textTooLong: "文字最多 {size}。", hostRequestFailed: "主机暂时无法处理请求。",
      removeMessageConfirm: "删除这条消息及其附件？", deleteFailed: "暂时无法删除，请重试。",
      activeTransferUnavailable: "暂时无法检查待续传消息，请刷新后重试。", deletePending: "删除中…",
      hostAddressUnavailable: "主机暂时没有可用的局域网地址。", copyFailed: "复制失败，请手动选择并复制。",
      languageChanged: "界面语言已切换。", localTitle: "本地运行，无需云服务器",
      localText: "文字和文件保存在运行主机上，不上传第三方云服务。", roomsTitle: "多个独立房间",
      roomsText: "不同房间各自拥有邀请链接、记录和保留期。", fileSizeTitle: "文件上限可调整",
      fileSizeText: "没有云端套餐限额；实际受主机磁盘空间约束。", retentionSaved: "保留时间已保存。",
      noRoomRecords: "本房间没有已发送的记录。", clearDone: "已删除 {count} 条记录。",
      clearRetry: "有 {count} 条暂时无法删除，请稍后重试。", noRoomsAvailable: "请使用新的邀请链接加入房间。",
      switchingRoom: "正在确认当前分片，随后切换房间", uploadPaused: "上传已暂停，可以继续发送。",
      roomRemovedToast: "房间已删除", roomUnavailable: "暂时无法连接主机，请重试。",
      deleteRoomConfirm: "删除「{name}」？历史记录、附件和未完成的上传都会被清除，邀请链接也会失效。{replacement}",
      defaultReplacement: " 将生成一个空的默认房间。", shortenRetentionConfirm: "将「{name}」的保留期改为 {days} 天？{count} 条记录将过期并被清理。",
      clearRoomConfirm: "清空「{name}」中已发送的 {count} 条记录及附件？此操作无法撤销。",
      roomAdminTitle: "房间管理 · {name}", expiredRowsChanged: "记录已变化，请重新查看将过期的条数。",
      expiredAttachmentBusy: "有过期的附件尚未释放，请等下载结束后重试。", roomNameInvalid: "请输入 1 至 40 个字符作为房间名称。",
      hostOnly: "请在运行互传程序的 Windows 主机上操作。", roomVersionInvalid: "房间编号或版本无效。",
      retentionInvalid: "请选择有效的保留时间。", clearTooMany: "本次清空的记录过多，请重新确认。",
      invalidRecordId: "记录编号无效，请重新确认。", quotaReached: "已达到额度上限。", chunkInvalid: "分片长度或位置无效。",
      attachmentsIncomplete: "附件尚未全部上传。", deviceDraftBusy: "这台设备已有一条待续传消息。请在原页面继续或取消后重新发送。",
      transferAlreadyPublished: "该传输已提交或编号已被使用，请查看记录。", apiRequestFailed: "请求失败，请重试。"
    },
    en: {
      product: "BendiSend · Local File Transfer", switchRoom: "Switch room", language: "Language", defaultRoom: "Default room",
      qrAlt: "QR code to join this room", messages: "Messages", downloadFile: "Download {name}",
      quotaMessage: "{name} limit is {limit}; this transfer needs {required}. {next}",
      diskSpace: "The host disk is low on space. Free up space and retry.",
      connecting: "Connecting", connected: "Host connected", unjoined: "Not joined", offline: "Host unavailable", reconnecting: "Reconnecting",
      openingRoom: "Opening room", pleaseWait: "Please wait.", retry: "Reconnect", joinInbox: "Join shared inbox",
      joinPrompt: "Scan the host's QR code or open an invitation link.", openFailed: "Unable to open room",
      openFailedPrompt: "Make sure the host app is running and both devices use the same Wi-Fi.", rejoin: "Please rejoin the room",
      rejoinPrompt: "Scan again or open the invitation link to continue.", roomDeleted: "Room deleted",
      roomDeletedPrompt: "Use a new invitation link or QR code to join a room.", connectDevices: "Connect devices", close: "Close",
      sameWifi: "Connect to the same Wi-Fi", scanPhone: "Scan with your phone", or: "or", copyInvite: "Copy invite link — open to join",
      hostNoAddress: "The host has no available LAN address.", inviteReadFailed: "Unable to load the invitation link.",
      devices: "Devices", self: "This device", inbox: "Shared inbox", retentionDays: "Keep records for {days} days",
      retentionForever: "Keep records indefinitely", manageRoom: "Room settings", transferLimits: "Transfer limits", timeline: "Message timeline",
      noMessages: "No messages yet", sendBelow: "Send text or files below.", editMessage: "Compose message",
      pendingAttachments: "Attachments", text: "Text", inputPlaceholder: "Paste text or drop files here…",
      uploadProgress: "Upload progress", cancelUpload: "Cancel upload", changeLimits: "Change limits", chooseFile: "Choose files",
      chooseOriginal: "Choose original file", dragFiles: "You can also drop files", enterHint: "Enter to send · Shift+Enter for a new line",
      send: "Send", sending: "Sending…", continueSending: "Continue", addRoom: "Add room", roomName: "Room name",
      roomExample: "For example: Work", cancel: "Cancel", createRoom: "Create room", retentionTime: "Record retention",
      oneDay: "1 day", sevenDays: "7 days", thirtyDays: "30 days", longTerm: "Indefinitely", save: "Save",
      retryClear: "Retry failed cleanup", clearRoom: "Clear this room", deleteRoom: "Delete room", limitsTitle: "Transfer limits",
      singleFileLimit: "Per-file limit (GiB)", sharedQuota: "Shared quota across rooms (GiB)", manualCopy: "Copy manually",
      manualCopyPrompt: "Long-press or use your system shortcut to copy the selected text.", downloaded: "Download", delete: "Delete", copy: "Copy",
      copied: "Text copied", today: "Today", attachment: "File", removeAttachment: "Remove attachment {name}", reselect: "Reselect",
      reselectFile: "Choose {name} again", fileRequiresReselect: "choose again", maxFiles: "A message can have up to {count} files.",
      resumeFileMismatch: "Choose the original files with the same names and sizes as the pending attachments.", pendingAvailable: "A pending transfer is ready to continue.",
      pendingNeedsFiles: "A pending transfer was found. Choose the original files to continue.", uploadedProgress: "Uploaded {current} / {total}",
      submittingMessage: "Submitting message", checkingFile: "Verifying “{name}”", verifyFileFailed: "Unable to verify the original file. Choose it again or refresh and retry.",
      uploadProgressChanged: "Upload progress changed on the host. Reconnect and continue.", fileContentMismatch: "“{name}” differs from the uploaded data. Choose the original file again.",
      disconnectedPreserved: "Connection lost. Confirmed chunks are preserved.", retryingUpload: "Connection lost or host busy. Retrying…",
      sent: "Sent", fileLimit: "Per-file limit", totalLimit: "Shared room quota",
      hostEditQuota: "Change the limit on the Windows host, then retry here.", pendingExpired: "The pending transfer expired. Choose the attachments again to send.",
      reconnectToResume: "Connection lost. Your draft and confirmed chunks are saved. Reconnect to continue.", resumeFailed: "Unable to continue the upload.",
      checkingQuota: "Checking transfer quota…", hostRestored: "Host is back. Choose the missing original files to continue.",
      submitting: "Submitting message…", uploadCanceled: "Upload canceled. You can still edit your draft.",
      cancelRequestFailed: "Could not notify the host to cancel. Reconnect and retry.", readingHostSpace: "Reading host storage…",
      saveAndContinue: "Save and continue", hostUsage: "Used {used} · Reserved for uploads {pending} · Disk free {free}",
      hostSpaceReadFailed: "Unable to read host storage. Please retry.", limitsInvalid: "Enter positive limits. The shared quota must be at least the per-file limit.",
      limitsSaved: "Transfer limits saved", limitsSaveFailed: "Unable to save. Please retry.", qrFailed: "QR code unavailable. You can copy the invitation link instead.",
      inviteCopied: "Invitation link copied", textTooLong: "Text is limited to {size}.", hostRequestFailed: "The host could not process the request.",
      removeMessageConfirm: "Delete this message and its attachments?", deleteFailed: "Unable to delete. Please retry.",
      activeTransferUnavailable: "Unable to check pending transfers. Refresh and retry.", deletePending: "Deleting…",
      hostAddressUnavailable: "The host has no available LAN address.", copyFailed: "Copy failed. Select and copy the text manually.",
      languageChanged: "Language changed.", localTitle: "Runs locally — no cloud server",
      localText: "Text and files stay on the host; nothing is uploaded to a third-party cloud service.", roomsTitle: "Multiple separate rooms",
      roomsText: "Each room has its own invitation link, history, and retention period.", fileSizeTitle: "Adjustable file limits",
      fileSizeText: "No cloud plan limits; actual size depends on the host's free disk space.", retentionSaved: "Retention period saved.",
      noRoomRecords: "This room has no published records.", clearDone: "Deleted {count} records.",
      clearRetry: "{count} records could not be deleted yet. Retry later.", noRoomsAvailable: "Use a new invitation link to join a room.",
      switchingRoom: "Finishing the current chunk before switching rooms", uploadPaused: "Upload paused. You can continue later.",
      roomRemovedToast: "Room deleted", roomUnavailable: "Unable to connect to the host. Please retry.",
      deleteRoomConfirm: "Delete “{name}”? Its history, attachments, and unfinished uploads will be removed, and its invitation will stop working.{replacement}",
      defaultReplacement: " A new empty Default room will be created.", shortenRetentionConfirm: "Change “{name}” retention to {days} days? {count} records will expire and be deleted.",
      clearRoomConfirm: "Clear {count} published records and attachments from “{name}”? This cannot be undone.",
      roomAdminTitle: "Room settings · {name}", expiredRowsChanged: "Records changed. Review the expiration count again.",
      expiredAttachmentBusy: "Some expired attachments are still in use. Wait for downloads to finish and retry.", roomNameInvalid: "Enter a room name between 1 and 40 characters.",
      hostOnly: "Use the Windows host running BendiSend to manage rooms.", roomVersionInvalid: "Invalid room ID or version.",
      retentionInvalid: "Choose a valid retention period.", clearTooMany: "Too many records selected. Review and confirm again.",
      invalidRecordId: "Invalid record ID. Review and confirm again.", quotaReached: "The storage quota has been reached.", chunkInvalid: "Invalid chunk size or offset.",
      attachmentsIncomplete: "Not all attachments have finished uploading.", deviceDraftBusy: "This device already has a pending transfer. Continue or cancel it in the original page before sending another.",
      transferAlreadyPublished: "This transfer was already submitted or its ID is in use. Check the message history.", apiRequestFailed: "Request failed. Please retry."
    }
  };

  const saved = localStorage.getItem("bendisend.language");
  let language = saved === "en" || saved === "zh-CN" ? saved : (navigator.language?.toLowerCase().startsWith("en") ? "en" : "zh-CN");

  function tr(key, values = {}) {
    if (!messages[language][key]) {
      const sourceKey = Object.keys(messages["zh-CN"]).find((candidate) => messages["zh-CN"][candidate] === key);
      if (sourceKey) key = sourceKey;
    }
    let value = messages[language][key] || messages["zh-CN"][key] || key;
    for (const [name, replacement] of Object.entries(values)) value = value.replaceAll(`{${name}}`, String(replacement));
    return value;
  }

  function apply() {
    document.documentElement.lang = language;
    document.title = tr("product");
    for (const node of document.querySelectorAll("[data-i18n]")) node.textContent = tr(node.dataset.i18n);
    for (const node of document.querySelectorAll("[data-i18n-placeholder]")) node.placeholder = tr(node.dataset.i18nPlaceholder);
    for (const node of document.querySelectorAll("[data-i18n-aria-label]")) node.setAttribute("aria-label", tr(node.dataset.i18nAriaLabel));
    for (const node of document.querySelectorAll("[data-i18n-alt]")) node.alt = tr(node.dataset.i18nAlt);
    const selector = document.getElementById("language-select");
    if (selector) selector.value = language;
  }

  window.t = tr;
  window.currentLanguage = () => language;
  window.setLanguage = (next) => {
    if (next !== "en" && next !== "zh-CN") return;
    if (next === language) return;
    language = next;
    localStorage.setItem("bendisend.language", language);
    apply();
    window.dispatchEvent(new Event("bendisend:languagechange"));
  };

  document.addEventListener("DOMContentLoaded", () => {
    apply();
    document.getElementById("language-select")?.addEventListener("change", (event) => window.setLanguage(event.target.value));
  });
})();
