// Web KVM 前端純原生控制器 (WebCodecs H.264 硬體解碼)
(function () {
    'use strict';

    // 取得 DOM 元件
    const canvas = document.getElementById('screenCanvas');
    const ctx = canvas.getContext('2d');
    const statusDot = document.getElementById('statusDot');
    const statusText = document.getElementById('statusText');
    const monitorSelect = document.getElementById('monitorSelect');
    const statsFps = document.getElementById('statsFps');
    const statsBitrate = document.getElementById('statsBitrate');
    const fullscreenBtn = document.getElementById('fullscreenBtn');
    const unsupportedOverlay = document.getElementById('unsupportedOverlay');
    const dropOverlay = document.getElementById('dropOverlay');
    const toast = document.getElementById('toast');
    const uploadModal = document.getElementById('uploadProgressModal');
    const uploadFileName = document.getElementById('uploadFileName');
    const uploadProgressBar = document.getElementById('uploadProgressBar');
    const uploadProgressPercent = document.getElementById('uploadProgressPercent');
    const uploadProgressDetail = document.getElementById('uploadProgressDetail');

    // 權限與身分相關 DOM
    const roleBadge = document.getElementById('roleBadge');
    const shortcutGroup = document.getElementById('shortcutGroup');
    const authOverlay = document.getElementById('authOverlay');
    const authPasswordInput = document.getElementById('authPasswordInput');
    const authLoginBtn = document.getElementById('authLoginBtn');
    const authErrorMsg = document.getElementById('authErrorMsg');
    const authRequestBtn = document.getElementById('authRequestBtn');
    const authPendingStatus = document.getElementById('authPendingStatus');
    const authPendingText = document.getElementById('authPendingText');

    // 檢查瀏覽器是否支援 WebCodecs API
    if (!('VideoDecoder' in window)) {
        unsupportedOverlay.classList.remove('hidden');
        statusText.textContent = '不支援 WebCodecs';
        return;
    }

    let ws = null;
    let decoder = null;
    let frameCounter = 0;
    let byteCounter = 0;
    let lastStatsTime = performance.now();
    let hasReceivedKeyframe = false;
    let currentToken = ''; // 純記憶體變數：重新整理或重開頁面時必然清空，強制重新驗證
    let currentRole = 0; // 0: None, 1: View (僅觀看), 2: Standard (一般控制), 3: Admin (完全控制)
    let lastSyncedText = '';
    let lastImageKey = ''; // 記錄本機最新同步圖片的特徵 (大小+類型)，防止重覆上傳
    let reconnectTimer = null;
    let authPollingTimer = null;

    // 依據授權身分角色套用前端介面自適應
    function applyRolePermissions(role, roleName) {
        currentRole = role;
        roleBadge.className = 'role-badge';

        if (role === 1) {
            // 第一級：僅觀看模式
            roleBadge.textContent = '👁️ 僅觀看模式';
            roleBadge.classList.add('role-view');
            if (shortcutGroup) shortcutGroup.style.display = 'none';
        } else if (role === 2) {
            // 第二級：一般控制
            roleBadge.textContent = '🎮 一般控制';
            roleBadge.classList.add('role-standard');
            if (shortcutGroup) {
                shortcutGroup.style.display = 'flex';
                // 隱藏管理員專屬組合鍵 (如 Ctrl+Alt+Del)
                document.querySelectorAll('.btn-admin').forEach(btn => btn.style.display = 'none');
            }
        } else if (role === 3) {
            // 第三級：完全控制 (系統管理員)
            roleBadge.textContent = '👑 完全控制(管理員)';
            roleBadge.classList.add('role-admin');
            if (shortcutGroup) {
                shortcutGroup.style.display = 'flex';
                // 解鎖全部系統按鈕
                document.querySelectorAll('.btn-admin').forEach(btn => btn.style.display = 'inline-block');
            }
        }

        // 隱藏認證遮罩
        if (authOverlay) authOverlay.classList.add('hidden');
        if (authPollingTimer) {
            clearInterval(authPollingTimer);
            authPollingTimer = null;
        }
    }

    // 顯示認證遮罩
    function showAuthOverlay() {
        if (!authOverlay) return;
        authOverlay.classList.remove('hidden');
        if (authPendingStatus) authPendingStatus.classList.add('hidden');
        if (authRequestBtn) authRequestBtn.style.display = 'block';
        if (authErrorMsg) authErrorMsg.classList.add('hidden');
        if (authPasswordInput) {
            authPasswordInput.value = '';
            authPasswordInput.focus();
        }
    }

    // 1. 密碼直接登入處理
    function submitPasswordAuth() {
        const pwd = authPasswordInput.value.trim();
        if (!pwd) {
            authErrorMsg.textContent = '請輸入存取密碼！';
            authErrorMsg.classList.remove('hidden');
            return;
        }

        authErrorMsg.classList.add('hidden');
        authLoginBtn.disabled = true;
        authLoginBtn.textContent = '驗證中...';

        fetch('/api/auth/login', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ password: pwd })
        })
        .then(res => res.json().then(data => ({ ok: res.ok, data })))
        .then(({ ok, data }) => {
            authLoginBtn.disabled = false;
            authLoginBtn.textContent = '連線';
            if (ok && data.status === 'ok') {
                currentToken = data.token;
                applyRolePermissions(data.role, data.role_name);
                showToast(`✅ 已授權：${data.role_name}`);
                connectWebSocket();
            } else {
                authErrorMsg.textContent = data.error || '密碼錯誤，請重新輸入！';
                authErrorMsg.classList.remove('hidden');
            }
        })
        .catch(err => {
            authLoginBtn.disabled = false;
            authLoginBtn.textContent = '連線';
            authErrorMsg.textContent = '伺服器連線失敗：' + err.message;
            authErrorMsg.classList.remove('hidden');
        });
    }

    authLoginBtn.addEventListener('click', submitPasswordAuth);
    authPasswordInput.addEventListener('keydown', (e) => {
        if (e.key === 'Enter') submitPasswordAuth();
    });

    // 2. 發起請求被控端核准
    authRequestBtn.addEventListener('click', () => {
        authRequestBtn.style.display = 'none';
        authPendingStatus.classList.remove('hidden');
        authPendingText.textContent = '已發送連線審核請求，等待被控端主人核准中...';
        authErrorMsg.classList.add('hidden');

        fetch('/api/auth/request', { method: 'POST' })
        .then(res => res.json())
        .then(data => {
            const reqId = data.request_id;
            // 啟動 1.5 秒定時輪詢核准狀態
            authPollingTimer = setInterval(() => {
                fetch(`/api/auth/status?id=${encodeURIComponent(reqId)}`)
                .then(res => res.json())
                .then(st => {
                    if (st.status === 1) { // Approved
                        clearInterval(authPollingTimer);
                        authPollingTimer = null;
                        currentToken = st.token;
                        applyRolePermissions(st.granted_role, st.role_name);
                        showToast(`🎉 被控端主人已核准連線：【${st.role_name}】`);
                        connectWebSocket();
                    } else if (st.status === 2) { // Rejected
                        clearInterval(authPollingTimer);
                        authPollingTimer = null;
                        authPendingStatus.classList.add('hidden');
                        authRequestBtn.style.display = 'block';
                        authErrorMsg.textContent = '❌ 被控端主人拒絕了您的連線請求。';
                        authErrorMsg.classList.remove('hidden');
                    } else if (st.status === 3) { // Expired
                        clearInterval(authPollingTimer);
                        authPollingTimer = null;
                        authPendingStatus.classList.add('hidden');
                        authRequestBtn.style.display = 'block';
                        authErrorMsg.textContent = '⏰ 連線請求已逾時，請重新發送。';
                        authErrorMsg.classList.remove('hidden');
                    }
                })
                .catch(() => {});
            }, 1500);
        })
        .catch(err => {
            authPendingStatus.classList.add('hidden');
            authRequestBtn.style.display = 'block';
            authErrorMsg.textContent = '發送連線請求失敗：' + err.message;
            authErrorMsg.classList.remove('hidden');
        });
    });

    // 1. 初始化 WebCodecs 硬體解碼器
    function initDecoder() {
        if (decoder && decoder.state !== 'closed') {
            try { decoder.close(); } catch (e) {}
        }
        hasReceivedKeyframe = false;

        decoder = new VideoDecoder({
            output: handleDecodedFrame,
            error: (err) => {
                console.error('[WebCodecs 解碼錯誤]', err);
                requestKeyframe();
            }
        });

        decoder.configure({
            codec: 'avc1.42E01F',
            optimizeForLatency: true
        });
        console.log('[WebCodecs] 原生 H.264 解碼器已就緒');
    }

    // 2. 處理解碼完成的視訊訊框並渲染至 Canvas
    function handleDecodedFrame(videoFrame) {
        if (canvas.width !== videoFrame.displayWidth || canvas.height !== videoFrame.displayHeight) {
            canvas.width = videoFrame.displayWidth;
            canvas.height = videoFrame.displayHeight;
        }

        ctx.drawImage(videoFrame, 0, 0, canvas.width, canvas.height);
        videoFrame.close();
        frameCounter++;
    }

    // 排程自動重新連線 (斷線後每 1.5 秒重試一次)
    function scheduleReconnect() {
        if (reconnectTimer) return;
        statusText.textContent = '連線中斷，正在自動重連...';
        statusDot.className = 'dot disconnected';
        reconnectTimer = setTimeout(() => {
            reconnectTimer = null;
            connectWebSocket();
        }, 1500);
    }

    // 3. 連接 WebSocket 伺服器
    function connectWebSocket() {
        if (ws) {
            try {
                ws.onopen = null;
                ws.onclose = null;
                ws.onerror = null;
                ws.onmessage = null;
                ws.close();
            } catch (e) {}
            ws = null;
        }

        const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
        let wsUrl = `${protocol}//${window.location.host}/ws`;
        if (currentToken) {
            wsUrl += `?token=${encodeURIComponent(currentToken)}`;
        }

        statusText.textContent = '連線中...';
        statusDot.className = 'dot disconnected';

        ws = new WebSocket(wsUrl);
        ws.binaryType = 'arraybuffer';

        ws.onopen = () => {
            statusText.textContent = '已連線 (串流中)';
            statusDot.className = 'dot connected';
            initDecoder();
            requestKeyframe();
        };

        ws.onclose = () => {
            scheduleReconnect();
        };

        ws.onerror = (err) => {
            console.warn('[WebSocket 異常]', err);
            scheduleReconnect();
        };

        ws.onmessage = (event) => {
            if (typeof event.data === 'string') {
                try {
                    const msg = JSON.parse(event.data);
                    handleControlMessage(msg);
                } catch (e) {
                    console.error('[JSON 解析失敗]', e);
                }
                return;
            }

            if (event.data instanceof ArrayBuffer) {
                handleBinaryVideoData(event.data);
            }
        };
    }

    // 4. 處理二進位視訊數據封包
    function handleBinaryVideoData(arrayBuffer) {
        const bytes = new Uint8Array(arrayBuffer);
        if (bytes.length < 5) return; // 協議最小長度：1(標誌) + 4(時間戳)

        byteCounter += bytes.length;

        const isKey = bytes[0] === 1;
        const dataView = new DataView(arrayBuffer);
        const timestampMs = dataView.getUint32(1, false);
        const naluData = bytes.subarray(5);

        // 如果解碼器尚未收到過第一個關鍵幀，略過無效的 P 幀以防止解碼破面
        if (!hasReceivedKeyframe) {
            if (!isKey) {
                return;
            }
            hasReceivedKeyframe = true;
        }

        try {
            const chunk = new EncodedVideoChunk({
                type: isKey ? 'key' : 'delta',
                timestamp: timestampMs * 1000,
                data: naluData
            });

            if (decoder && decoder.state === 'configured') {
                decoder.decode(chunk);
            }
        } catch (e) {
            console.warn('[訊框解碼略過]', e);
        }
    }

    // 5. 處理後端控制訊息
    function handleControlMessage(msg) {
        if (msg.type === 'auth_success') {
            // 身分驗證通過
            applyRolePermissions(msg.role, msg.role_name);
        } else if (msg.type === 'auth_fail') {
            // Token 失效或被踢除
            currentToken = '';
            currentRole = 0;
            showAuthOverlay();
            showToast('⚠️ 連線憑證已失效，請重新授權');
        } else if (msg.type === 'init') {
            // 初始化螢幕選單清單
            monitorSelect.innerHTML = '';
            msg.monitors.forEach(m => {
                const opt = document.createElement('option');
                opt.value = m.index;
                const pri = m.is_primary ? ' [主螢幕]' : '';
                const rot = m.rotation === 2 ? ' (倒置180°)' : '';
                opt.textContent = `螢幕 ${m.index}: ${m.width}x${m.height}${rot}${pri}`;
                monitorSelect.appendChild(opt);
            });
            monitorSelect.value = msg.current;
        } else if (msg.type === 'clipboard_sync' || msg.type === 'clipboard_data') {
            lastSyncedText = msg.text || '';
            if (navigator.clipboard && navigator.clipboard.writeText) {
                navigator.clipboard.writeText(lastSyncedText).catch(() => {});
            }
        } else if (msg.type === 'clipboard_image_sync') {
            try {
                const byteChars = atob(msg.data);
                const byteNumbers = new Array(byteChars.length);
                for (let i = 0; i < byteChars.length; i++) {
                    byteNumbers[i] = byteChars.charCodeAt(i);
                }
                const byteArray = new Uint8Array(byteNumbers);
                const blob = new Blob([byteArray], { type: 'image/png' });

                if (navigator.clipboard && navigator.clipboard.write) {
                    navigator.clipboard.write([new ClipboardItem({ 'image/png': blob })])
                        .then(() => showToast('🖼️ 遠端截圖已自動同步至本機剪貼簿'))
                        .catch(() => {});
                }
            } catch (e) {
                console.error('[圖片剪貼簿解析失敗]', e);
            }
        }
    }

    // 請求伺服器立即產生一個關鍵幀 (I 幀)
    function requestKeyframe() {
        if (ws && ws.readyState === WebSocket.OPEN) {
            ws.send(JSON.stringify({ type: 'request_keyframe' }));
        }
    }

    // 6. 計算滑鼠在 Canvas 視訊內容中的精確相對歸一化座標 (0.0 ~ 1.0)
    function getCanvasCoordinates(e) {
        const rect = canvas.getBoundingClientRect();
        if (rect.width <= 0 || rect.height <= 0) return null;

        const clickX = e.clientX - rect.left;
        const clickY = e.clientY - rect.top;

        let normX = clickX / rect.width;
        let normY = clickY / rect.height;

        if (normX < 0) normX = 0;
        if (normX > 1) normX = 1;
        if (normY < 0) normY = 0;
        if (normY > 1) normY = 1;

        return { normX, normY };
    }

    // 發送控制指令至 WebSocket
    function sendControl(data) {
        if (ws && ws.readyState === WebSocket.OPEN) {
            ws.send(JSON.stringify(data));
        }
    }

    // 7. 滑鼠事件監聽 (僅在角色具備 RoleStandard 以上時允許操作)
    let moveThrottle = false;
    canvas.addEventListener('mousemove', (e) => {
        if (currentRole < 2) return; // 僅觀看模式：禁止滑鼠操作
        if (moveThrottle) return;

        moveThrottle = true;
        requestAnimationFrame(() => {
            moveThrottle = false;
            const pos = getCanvasCoordinates(e);
            if (pos) {
                sendControl({
                    type: 'mouse_move',
                    x: pos.normX,
                    y: pos.normY
                });
            }
        });
    });

    canvas.addEventListener('mousedown', (e) => {
        if (currentRole < 2) return;
        canvas.focus();

        const pos = getCanvasCoordinates(e);
        if (pos) {
            sendControl({ type: 'mouse_move', x: pos.normX, y: pos.normY });
            sendControl({
                type: 'mouse_button',
                button: e.button,
                isDown: true
            });
        }
    });

    canvas.addEventListener('mouseup', (e) => {
        if (currentRole < 2) return;
        sendControl({
            type: 'mouse_button',
            button: e.button,
            isDown: false
        });
    });

    canvas.addEventListener('contextmenu', (e) => {
        if (currentRole >= 2) {
            e.preventDefault();
        }
    });

    canvas.addEventListener('wheel', (e) => {
        if (currentRole < 2) return;
        e.preventDefault();
        sendControl({
            type: 'mouse_wheel',
            deltaX: Math.round(e.deltaX),
            deltaY: Math.round(e.deltaY)
        });
    }, { passive: false });

    // 8. 鍵盤事件監聽 (僅在角色具備 RoleStandard 以上時允許操作)
    window.addEventListener('keydown', (e) => {
        if (currentRole < 2) return;
        if (e.target.tagName === 'INPUT' || e.target.tagName === 'SELECT') return;

        if (['Tab', 'Backspace', 'ContextMenu', 'AltLeft', 'AltRight'].includes(e.code) || e.code.startsWith('F')) {
            e.preventDefault();
        }

        if ((e.ctrlKey || e.metaKey) && e.code === 'KeyV') {
            syncClipboardText();
            checkAndSyncLocalClipboardImage();
        }

        sendControl({
            type: 'key',
            code: e.code,
            isDown: true
        });
    });

    window.addEventListener('keyup', (e) => {
        if (currentRole < 2) return;
        if (e.target.tagName === 'INPUT' || e.target.tagName === 'SELECT') return;

        sendControl({
            type: 'key',
            code: e.code,
            isDown: false
        });
    });

    // 檢查並同步本機剪貼簿圖片至遠端主機
    async function checkAndSyncLocalClipboardImage() {
        if (currentRole < 2) return;
        if (!navigator.clipboard || !navigator.clipboard.read) return;
        try {
            const items = await navigator.clipboard.read();
            for (const item of items) {
                const imageType = item.types.find(t => t.startsWith('image/'));
                if (imageType) {
                    const blob = await item.getType(imageType);
                    const imageKey = `${blob.size}_${blob.type}`;
                    if (imageKey !== lastImageKey) {
                        lastImageKey = imageKey;
                        const reader = new FileReader();
                        reader.onload = () => {
                            const base64 = reader.result.split(',')[1];
                            sendControl({
                                type: 'clipboard_image_upload',
                                data: base64
                            });
                            showToast('🖼️ 截圖已自動同步至遠端剪貼簿 (直接按 Ctrl+V 貼上)');
                        };
                        reader.readAsDataURL(blob);
                    }
                    break;
                }
            }
        } catch (e) {}
    }

    // 檢查並同步純文字剪貼簿
    function syncClipboardText() {
        if (currentRole < 2) return;
        if (navigator.clipboard && navigator.clipboard.readText) {
            navigator.clipboard.readText().then(text => {
                if (text && text !== lastSyncedText) {
                    lastSyncedText = text;
                    sendControl({ type: 'clipboard_sync', text: text });
                }
            }).catch(() => {});
        }
    }

    canvas.addEventListener('mouseenter', () => {
        checkAndSyncLocalClipboardImage();
    });

    window.addEventListener('focus', () => {
        syncClipboardText();
        checkAndSyncLocalClipboardImage();
    });

    window.addEventListener('blur', () => {
        if (currentRole >= 2) {
            sendControl({ type: 'release_all_keys' });
        }
    });

    // 9. KVM 專屬系統快捷鍵按鈕
    document.querySelectorAll('.btn-shortcut').forEach(btn => {
        btn.addEventListener('click', () => {
            // 特權組合鍵 (Ctrl+Alt+Del) 必須具備 RoleAdmin (3) 權限
            if (btn.classList.contains('btn-admin') && currentRole < 3) {
                showToast('🔒 該特權組合鍵僅限「完全控制(管理員)」使用');
                return;
            }

            const keyName = btn.dataset.key;
            if (keyName) {
                sendControl({
                    type: 'special_key',
                    key: keyName
                });
                canvas.focus();
            }
        });
    });

    // 11. 圖片剪貼簿貼上事件備援 (監聽本機複製圖片並在 KVM 視窗按下 Ctrl+V)
    window.addEventListener('paste', (e) => {
        if (!controlEnabled) return;
        const items = e.clipboardData && e.clipboardData.items;
        if (!items) return;

        for (let i = 0; i < items.length; i++) {
            if (items[i].type.startsWith('image/')) {
                const blob = items[i].getAsFile();
                if (blob) {
                    const reader = new FileReader();
                    reader.onload = () => {
                        const base64 = reader.result.split(',')[1];
                        sendControl({
                            type: 'clipboard_image_upload',
                            data: base64
                        });
                        showToast('🖼️ 截圖已同步至遠端剪貼簿 (正在貼上)');
                    };
                    reader.readAsDataURL(blob);
                    break;
                }
            }
        }
    });

    // 12. 檔案無縫拖曳傳送至遠端電腦桌面 (具備即時進度條、傳輸速度與防破損保護)
    function formatBytes(bytes) {
        if (!bytes || bytes <= 0) return '0 B';
        if (bytes < 1024) return bytes + ' B';
        if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + ' KB';
        return (bytes / (1024 * 1024)).toFixed(1) + ' MB';
    }

    function uploadFiles(files) {
        if (!files || files.length === 0) return;
        if (currentRole < 2) {
            showToast('🔒 僅觀看模式禁止傳送檔案');
            return;
        }

        const formData = new FormData();
        let totalSize = 0;
        for (let i = 0; i < files.length; i++) {
            formData.append('files', files[i]);
            totalSize += files[i].size;
        }

        const displayName = files.length === 1 ? files[0].name : `${files[0].name} 等 ${files.length} 個檔案`;
        uploadFileName.textContent = displayName;
        uploadProgressBar.style.width = '0%';
        uploadProgressBar.style.background = 'linear-gradient(90deg, #1f6feb, #58a6ff)';
        uploadProgressPercent.textContent = '0%';
        uploadProgressDetail.textContent = `0 MB / ${formatBytes(totalSize)} (計算速度中...)`;
        uploadModal.classList.remove('hidden');

        const xhr = new XMLHttpRequest();
        let lastLoaded = 0;
        let lastTime = performance.now();

        xhr.upload.onprogress = (e) => {
            if (e.lengthComputable && e.total > 0) {
                const now = performance.now();
                const percent = Math.min(100, (e.loaded / e.total * 100)).toFixed(1);
                uploadProgressBar.style.width = percent + '%';
                uploadProgressPercent.textContent = percent + '%';

                const timeDiff = (now - lastTime) / 1000;
                if (timeDiff >= 0.25) {
                    const bytesDiff = e.loaded - lastLoaded;
                    const speed = bytesDiff / timeDiff;
                    uploadProgressDetail.textContent = `${formatBytes(e.loaded)} / ${formatBytes(e.total)} (${formatBytes(speed)}/s)`;
                    lastLoaded = e.loaded;
                    lastTime = now;
                }
            }
        };

        xhr.onload = () => {
            if (xhr.status >= 200 && xhr.status < 300) {
                uploadProgressBar.style.width = '100%';
                uploadProgressBar.style.background = 'linear-gradient(90deg, #238636, #2ea043)';
                uploadProgressPercent.textContent = '100%';
                uploadProgressDetail.textContent = `✅ 傳輸成功！已存入遠端電腦桌面`;
                showToast(`✅ ${displayName} 已成功傳送至遠端桌面！`);
                setTimeout(() => {
                    uploadModal.classList.add('hidden');
                }, 2000);
            } else {
                uploadProgressBar.style.background = '#f85149';
                uploadProgressDetail.textContent = `❌ 傳輸失敗 (${xhr.status})`;
                showToast(`❌ 檔案傳送失敗: 權限不足或伺服器錯誤`);
                setTimeout(() => {
                    uploadModal.classList.add('hidden');
                }, 3000);
            }
        };

        xhr.onerror = () => {
            uploadProgressBar.style.background = '#f85149';
            uploadProgressDetail.textContent = '❌ 網路異常，傳輸中斷 (未損壞遠端檔案)';
            showToast(`❌ 檔案傳送中斷`);
            setTimeout(() => {
                uploadModal.classList.add('hidden');
            }, 3000);
        };

        xhr.open('POST', `/upload?token=${encodeURIComponent(currentToken)}`, true);
        xhr.send(formData);
    }

    const viewport = document.getElementById('viewport');

    window.addEventListener('dragenter', (e) => {
        if (currentRole < 2) return;
        e.preventDefault();
        if (dropOverlay) dropOverlay.classList.remove('hidden');
    });

    window.addEventListener('dragover', (e) => {
        if (currentRole < 2) return;
        e.preventDefault();
    });

    window.addEventListener('dragleave', (e) => {
        if (currentRole < 2) return;
        if (e.relatedTarget === null || e.clientX <= 0 || e.clientY <= 0) {
            if (dropOverlay) dropOverlay.classList.add('hidden');
        }
    });

    window.addEventListener('drop', (e) => {
        if (currentRole < 2) return;
        e.preventDefault();
        if (dropOverlay) dropOverlay.classList.add('hidden');

        const files = e.dataTransfer.files;
        if (files && files.length > 0) {
            uploadFiles(files);
        }
    });

    // 浮動訊息通知 (Toast)
    let toastTimer = null;
    function showToast(text) {
        if (!toast) return;
        toast.textContent = text;
        toast.classList.remove('hidden');
        clearTimeout(toastTimer);
        toastTimer = setTimeout(() => {
            toast.classList.add('hidden');
        }, 3000);
    }

    // 螢幕切換監聽
    monitorSelect.addEventListener('change', () => {
        const targetIdx = parseInt(monitorSelect.value, 10);
        sendControl({
            type: 'switch_monitor',
            index: targetIdx
        });
        initDecoder();
    });

    // 全螢幕按鈕
    fullscreenBtn.addEventListener('click', () => {
        if (!document.fullscreenElement) {
            canvas.requestFullscreen().catch(err => {
                alert(`無法進入全螢幕模式: ${err.message}`);
            });
        } else {
            document.exitFullscreen();
        }
    });

    // 11. 即時性能統計計時器 (每秒更新一次 FPS 與 Bitrate)
    setInterval(() => {
        const now = performance.now();
        const durationSec = (now - lastStatsTime) / 1000.0;
        if (durationSec > 0) {
            const currentFps = (frameCounter / durationSec).toFixed(1);
            const currentKbps = ((byteCounter * 8) / 1024.0 / durationSec).toFixed(0);

            statsFps.textContent = `${currentFps} FPS`;
            statsBitrate.textContent = `${currentKbps} Kbps`;
        }

        frameCounter = 0;
        byteCounter = 0;
        lastStatsTime = now;
    }, 1000);

    // 每次開啟頁面或重新整理（F5），一律強制重新驗證身分（Token 不持久化，僅留存於記憶體供 WebSocket 自動重連使用）
    try { localStorage.removeItem('webkvm_token'); } catch (e) {}
    currentToken = '';
    currentRole = 0;
    showAuthOverlay();
})();
