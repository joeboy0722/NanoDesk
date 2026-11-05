#include "decoder.h"
#include "native.h"
#include <algorithm>
#include <cmath>
#include <commctrl.h>
#include <commdlg.h>
#include <cstring>
#include <deque>
#include <map>
#include <memory>
#include <shellscalingapi.h>
#include <string>
#include <vector>
#include <windows.h>
#include <windowsx.h>

static HHOOK keyboardHook = nullptr;
static HINSTANCE instance;
static double uiScale = 1;
static int S(int v) { return int(v * uiScale + .5); }
static HFONT font, titleFont, smallFont, idFont;
static HBRUSH paperBrush, shellBrush;
static bool highContrast;
static HWND connectTitle, connectButton, copyID, idCaption, homeHint;
static std::wstring localID;
static constexpr COLORREF paper = RGB(255, 255, 255), shell = RGB(245, 244, 241),
                          ink = RGB(32, 38, 46), blue = RGB(36, 88, 211),
                          muted = RGB(87, 97, 111), edge = RGB(217, 220, 225),
                          canvasBackground = RGB(22, 26, 32);
static HWND modeCombo, closeRemote, settingsPane;
static int settingsScroll;
static HWND mainWindow, tabs, gear, closeTab, idText, statusText, targetEdit, inboundText, category,
    saveSettings;
static HWND serverEdit, caEdit, viewEdit, standardEdit, adminEdit, limitEdit, unattendedCheck, clipboardCheck;
static bool running = true, settingsOpen = false, dirty = false, loading = false;
static int inboundCount;
static int selectedPage = 0, selectedCategory = 0;
static std::deque<NDEvent> events;
static std::vector<HWND> homeControls, settingsControls;
struct Field {
    HWND hwnd;
    int category;
    RECT bounds;
};
static std::vector<Field> fields;
struct Icon {
    std::wstring glyph, label;
    bool selected = false;
    int monitor = -1;
    int state = 0;
};
static std::map<HWND, Icon> icons;
struct Remote {
    std::string sid;
    std::wstring targetID;
    HWND detached = nullptr, detachedTabs = nullptr;
    std::wstring phase = L"連線中", route;
    HWND chatWindow = nullptr, chatButton = nullptr;
    HWND infoPanel = nullptr, membersPanel = nullptr;
    HWND hwnd = nullptr, viewport = nullptr, canvas = nullptr, monitors = nullptr, tooltip = nullptr,
         status = nullptr, password = nullptr, authLabel = nullptr, request = nullptr, login = nullptr;
    std::vector<HWND> tools, screens;
    std::unique_ptr<Decoder> decoder;
    int role = 0;
    unsigned buttonsDown = 0;
    bool active = false, clip = false, laser = false, fullscreen = false;
    double zoom = 0;
    int scrollX = 0, scrollY = 0, monitorScroll = 0, sourceWidth = 0, sourceHeight = 0;
    RECT restore = {};
    LONG_PTR style = 0;
    std::wstring info = L"尚未連線", members = L"";
    ULONGLONG lastFrame = 0, lastKeyRequest = 0, noticeUntil = 0;
    bool authenticated = false, ended = false;
};
struct Pointer {
    double x, y;
    std::wstring name;
    COLORREF color;
    ULONGLONG at;
};
static std::map<std::string, std::map<std::string, Pointer>> pointers;
static std::map<std::string, std::unique_ptr<Remote>> remotes;
static std::vector<Remote *> docked;
static Remote *selectedRemote = nullptr;
static Remote *mainFullscreen = nullptr;
static void layout(Remote *r);
static void layoutHost(HWND host, Remote *r);
static void layoutSettings();
static void fullscreen(Remote *r);
static void syncInput();
static void openChat(Remote *r);
struct Approval {
    std::string sid, request;
    HWND hwnd;
    ULONGLONG start;
};
static std::map<HWND, Approval> approvals;
static std::wstring wide(const char *s) {
    if (!s)
        return L"";
    int n = MultiByteToWideChar(CP_UTF8, 0, s, -1, nullptr, 0);
    if (n <= 0)
        return L"";
    std::wstring out(n, L'\0');
    MultiByteToWideChar(CP_UTF8, 0, s, -1, &out[0], n);
    out.pop_back();
    return out;
}
static std::string utf8(const std::wstring &s) {
    int n = WideCharToMultiByte(CP_UTF8, 0, s.data(), (int)s.size(), nullptr, 0, nullptr, nullptr);
    std::string out(n, '\0');
    if (n)
        WideCharToMultiByte(CP_UTF8, 0, s.data(), (int)s.size(), &out[0], n, nullptr, nullptr);
    return out;
}
static std::wstring text(HWND h) {
    int n = GetWindowTextLengthW(h);
    std::wstring s(n + 1, L'\0');
    GetWindowTextW(h, &s[0], n + 1);
    s.resize(n);
    return s;
}
static std::string escape(const std::wstring &s) {
    std::string in = utf8(s), out = "\"";
    for (unsigned char c : in) {
        switch (c) {
        case '\\':
            out += "\\\\";
            break;
        case '"':
            out += "\\\"";
            break;
        case '\n':
            out += "\\n";
            break;
        case '\r':
            out += "\\r";
            break;
        case '\t':
            out += "\\t";
            break;
        default:
            if (c >= 32)
                out += char(c);
        }
    }
    return out + "\"";
}
static void push(int kind, const std::string &sid = "", const std::string &message = "", int a = 0, int b = 0,
                 double x = 0, double y = 0) {
    NDEvent e = {};
    e.kind = kind;
    e.a = a;
    e.b = b;
    e.x = x;
    e.y = y;
    strncpy(e.session, sid.c_str(), sizeof(e.session) - 1);
    strncpy(e.text, message.c_str(), sizeof(e.text) - 1);
    if (kind == 20 && !events.empty() && events.back().kind == 20 &&
        std::string(events.back().session) == sid) {
        events.back() = e;
        return;
    }
    if (kind == 20 && events.size() > 512)
        return;
    events.push_back(e);
}
static LRESULT CALLBACK tabPaintProc(HWND, UINT, WPARAM, LPARAM, UINT_PTR, DWORD_PTR);
static void command(Remote *r, const std::string &s) { push(25, r->sid, s); }
static HWND control(HWND parent, const wchar_t *cls, const wchar_t *label, DWORD style, int id, int x, int y,
                    int w, int h) {
    if (wcscmp(cls, L"BUTTON") == 0 && (style & BS_TYPEMASK) <= BS_DEFPUSHBUTTON)
        style = (style & ~BS_TYPEMASK) | BS_OWNERDRAW;
    if (wcscmp(cls, WC_TABCONTROLW) == 0)
        style |= TCS_OWNERDRAWFIXED | WS_CLIPCHILDREN;
    if (wcscmp(cls, L"LISTBOX") == 0)
        style |= LBS_OWNERDRAWFIXED | LBS_HASSTRINGS | LBS_NOINTEGRALHEIGHT;
    HWND c = CreateWindowExW(0, cls, label, WS_CHILD | WS_VISIBLE | style, S(x), S(y), S(w), S(h), parent,
                             (HMENU)(INT_PTR)id, instance, nullptr);
    SendMessageW(c, WM_SETFONT, (WPARAM)font, TRUE);
    if (wcscmp(cls, L"LISTBOX") == 0)
        SendMessageW(c, LB_SETITEMHEIGHT, 0, S(36));
    if (wcscmp(cls, L"EDIT") == 0 && !(style & ES_MULTILINE))
        SendMessageW(c, EM_SETMARGINS, EC_LEFTMARGIN | EC_RIGHTMARGIN, MAKELPARAM(S(8), S(8)));
    if (wcscmp(cls, WC_TABCONTROLW) == 0) {
        TabCtrl_SetPadding(c, S(14), S(8));
        SetWindowSubclass(c, tabPaintProc, 2, 0);
    }
    return c;
}
static HWND label(HWND p, const wchar_t *s, int x, int y, int w, int h) {
    return control(p, L"STATIC", s, 0, 0, x, y, w, h);
}
static void tip(HWND tooltip, HWND button, const std::wstring &s) {
    TOOLINFOW ti = {};
    ti.cbSize = sizeof(ti);
    ti.uFlags = TTF_IDISHWND | TTF_SUBCLASS;
    ti.hwnd = GetParent(button);
    ti.uId = (UINT_PTR)button;
    ti.lpszText = (LPWSTR)s.c_str();
    SendMessageW(tooltip, TTM_ADDTOOLW, 0, (LPARAM)&ti);
}
static HWND icon(HWND p, HWND tooltip, int id, const std::wstring &glyph, const std::wstring &name, int x,
                 int y, int w = 36) {
    HWND h = control(p, L"BUTTON", name.c_str(), BS_OWNERDRAW | WS_TABSTOP, id, x, y, w, 34);
    icons[h] = {glyph, name, false, -1};
    if (tooltip)
        tip(tooltip, h, icons[h].label);
    return h;
}
static Remote *remote(HWND h) { return (Remote *)GetWindowLongPtrW(h, GWLP_USERDATA); }
static Remote *find(const char *sid) {
    auto it = remotes.find(sid ? sid : "");
    return it == remotes.end() ? nullptr : it->second.get();
}
static void updateTip(Remote *r, HWND button, const std::wstring &name) {
    icons[button].label = name;
    SetWindowTextW(button, name.c_str());
    TOOLINFOW ti = {};
    ti.cbSize = sizeof(ti);
    ti.uFlags = TTF_IDISHWND;
    ti.hwnd = GetParent(button);
    ti.uId = (UINT_PTR)button;
    ti.lpszText = (LPWSTR)icons[button].label.c_str();
    SendMessageW(r->tooltip, TTM_UPDATETIPTEXTW, 0, (LPARAM)&ti);
}
static void fill(HDC dc, RECT rect, COLORREF color) {
    SetDCBrushColor(dc, color);
    FillRect(dc, &rect, (HBRUSH)GetStockObject(DC_BRUSH));
}

static void drawButton(DRAWITEMSTRUCT *d) {
    RECT r = d->rcItem;
    bool pressed = (d->itemState & ODS_SELECTED) != 0;
    bool disabled = (d->itemState & ODS_DISABLED) != 0;
    bool primary = d->CtlID == 106 || d->CtlID == 1100 || d->CtlID == 503 || d->CtlID == 201;
    bool danger = d->CtlID == 504 || d->CtlID == 304;
    COLORREF bg = primary && !disabled ? (pressed ? RGB(24, 63, 162) : blue) :
                  pressed ? RGB(234, 239, 250) : paper;
    COLORREF fg = disabled ? muted : primary ? paper : danger ? RGB(171, 38, 49) : ink;
    if (highContrast) {
        bg = GetSysColor(pressed ? COLOR_HIGHLIGHT : COLOR_BTNFACE);
        fg = GetSysColor(disabled ? COLOR_GRAYTEXT : pressed ? COLOR_HIGHLIGHTTEXT : COLOR_BTNTEXT);
    }
    FillRect(d->hDC, &r, highContrast ? GetSysColorBrush(COLOR_WINDOW) : paperBrush);
    int saved = SaveDC(d->hDC);
    SelectObject(d->hDC, GetStockObject(DC_BRUSH));
    SelectObject(d->hDC, GetStockObject(DC_PEN));
    SetDCBrushColor(d->hDC, bg);
    SetDCPenColor(d->hDC, highContrast ? fg : primary && !disabled ? bg : edge);
    RoundRect(d->hDC, r.left, r.top, r.right, r.bottom, S(12), S(12));
    SelectObject(d->hDC, font);
    SetBkMode(d->hDC, TRANSPARENT);
    SetTextColor(d->hDC, fg);
    auto title = text(d->hwndItem);
    DrawTextW(d->hDC, title.c_str(), int(title.size()), &r, DT_CENTER | DT_VCENTER | DT_SINGLELINE | DT_NOPREFIX);
    if (d->itemState & ODS_FOCUS) {
        InflateRect(&r, -S(4), -S(4));
        DrawFocusRect(d->hDC, &r);
    }
    RestoreDC(d->hDC, saved);
}

static void drawTab(DRAWITEMSTRUCT *d) {
    wchar_t title[192] = {};
    TCITEMW item = {};
    item.mask = TCIF_TEXT;
    item.pszText = title;
    item.cchTextMax = int(std::size(title));
    TabCtrl_GetItem(d->hwndItem, d->itemID, &item);
    bool selected = int(d->itemID) == TabCtrl_GetCurSel(d->hwndItem);
    RECT r = d->rcItem;
    fill(d->hDC, r, highContrast ? GetSysColor(selected ? COLOR_HIGHLIGHT : COLOR_WINDOW) :
                                  selected ? paper : shell);
    if (selected && !highContrast)
        fill(d->hDC, {r.left, r.top, r.right, r.top + S(2)}, blue);
    SetBkMode(d->hDC, TRANSPARENT);
    SetTextColor(d->hDC, highContrast ? GetSysColor(selected ? COLOR_HIGHLIGHTTEXT : COLOR_WINDOWTEXT) :
                                      selected ? blue : ink);
    HGDIOBJ previous = SelectObject(d->hDC, font);
    InflateRect(&r, -S(6), 0);
    DrawTextW(d->hDC, title, -1, &r, DT_CENTER | DT_VCENTER | DT_SINGLELINE | DT_END_ELLIPSIS | DT_NOPREFIX);
    if (d->itemState & ODS_FOCUS)
        DrawFocusRect(d->hDC, &r);
    SelectObject(d->hDC, previous);
}

static LRESULT CALLBACK tabPaintProc(HWND h, UINT msg, WPARAM w, LPARAM l, UINT_PTR, DWORD_PTR) {
    if (msg == WM_ERASEBKGND)
        return 1;
    if (msg != WM_PAINT && msg != WM_PRINTCLIENT)
        return DefSubclassProc(h, msg, w, l);
    PAINTSTRUCT ps;
    HDC dc = msg == WM_PAINT ? BeginPaint(h, &ps) : (HDC)w;
    RECT rect;
    GetClientRect(h, &rect);
    FillRect(dc, &rect, highContrast ? GetSysColorBrush(COLOR_WINDOW) : shellBrush);
    const int count = TabCtrl_GetItemCount(h), selection = TabCtrl_GetCurSel(h);
    for (int i = 0; i < count; ++i) {
        DRAWITEMSTRUCT item = {};
        item.hDC = dc;
        item.hwndItem = h;
        item.itemID = i;
        if (!TabCtrl_GetItemRect(h, i, &item.rcItem))
            continue;
        item.rcItem.bottom = rect.bottom;
        if (selection == i && GetFocus() == h)
            item.itemState = ODS_FOCUS;
        drawTab(&item);
    }
    if (msg == WM_PAINT)
        EndPaint(h, &ps);
    return 0;
}

static void drawCategory(DRAWITEMSTRUCT *item) {
    if (item->itemID == UINT(-1))
        return;
    wchar_t name[128] = {};
    SendMessageW(item->hwndItem, LB_GETTEXT, item->itemID, (LPARAM)name);
    bool selected = (item->itemState & ODS_SELECTED) != 0;
    RECT r = item->rcItem;
    fill(item->hDC, r, highContrast ? GetSysColor(selected ? COLOR_HIGHLIGHT : COLOR_WINDOW) :
                                     selected ? RGB(232, 239, 255) : shell);
    SetBkMode(item->hDC, TRANSPARENT);
    SetTextColor(item->hDC, highContrast ? GetSysColor(selected ? COLOR_HIGHLIGHTTEXT : COLOR_WINDOWTEXT) :
                                          selected ? blue : ink);
    HGDIOBJ previous = SelectObject(item->hDC, font);
    r.left += S(12);
    DrawTextW(item->hDC, name, -1, &r, DT_VCENTER | DT_SINGLELINE | DT_END_ELLIPSIS | DT_NOPREFIX);
    if (item->itemState & ODS_FOCUS)
        DrawFocusRect(item->hDC, &item->rcItem);
    SelectObject(item->hDC, previous);
}

static void drawIcon(DRAWITEMSTRUCT *d) {
    auto found = icons.find(d->hwndItem);
    if (found == icons.end()) {
        drawButton(d);
        return;
    }
    Icon &v = found->second;
    RECT r = d->rcItem;
    bool selected = v.selected || (d->itemState & ODS_SELECTED);
    fill(d->hDC, r, highContrast ? GetSysColor(selected ? COLOR_HIGHLIGHT : COLOR_WINDOW) :
                                  selected ? RGB(232, 239, 255) : paper);
    COLORREF color = highContrast ? GetSysColor(selected ? COLOR_HIGHLIGHTTEXT : COLOR_WINDOWTEXT) :
                     v.state == 2 || d->CtlID == 218 ? RGB(171, 38, 49) :
                     (d->itemState & ODS_DISABLED) ? muted : selected ? blue : ink;
    SetBkMode(d->hDC, TRANSPARENT);
    SetTextColor(d->hDC, color);
    SelectObject(d->hDC, font);
    HPEN pen = CreatePen(PS_SOLID, std::max(1, S(2)), color);
    HGDIOBJ oldPen = SelectObject(d->hDC, pen), oldBrush = SelectObject(d->hDC, GetStockObject(NULL_BRUSH));
    int cx = (r.left + r.right) / 2, cy = (r.top + r.bottom) / 2, u = S(1);
    auto line = [&](int x1, int y1, int x2, int y2) {
        MoveToEx(d->hDC, cx + x1 * u, cy + y1 * u, nullptr);
        LineTo(d->hDC, cx + x2 * u, cy + y2 * u);
    };
    auto box = [&](int x1, int y1, int x2, int y2) {
        Rectangle(d->hDC, cx + x1 * u, cy + y1 * u, cx + x2 * u, cy + y2 * u);
    };
    auto circle = [&](int x, int y, int radius) {
        Ellipse(d->hDC, cx + (x - radius) * u, cy + (y - radius) * u, cx + (x + radius) * u,
                cy + (y + radius) * u);
    };
    if (v.monitor >= 0) {
        RECT rect = {r.left + S(6), r.top + S(4), r.right - S(6), r.bottom - S(8)};
        Rectangle(d->hDC, rect.left, rect.top, rect.right, rect.bottom);
        DrawTextW(d->hDC, v.glyph.c_str(), -1, &rect, DT_CENTER | DT_VCENTER | DT_SINGLELINE);
        line(0, 9, 0, 12);
        line(-6, 12, 6, 12);
    } else if (v.state == 1) {
        circle(0, 0, 10);
        line(0, -6, 0, 0);
        line(0, 0, 5, 3);
    } else if (v.state == 2) {
        circle(0, 0, 10);
        line(0, -6, 0, 2);
        line(0, 5, 0, 7);
    } else
        switch (GetDlgCtrlID(d->hwndItem)) {
        case 101:
            circle(0, 0, 7);
            circle(0, 0, 3);
            for (int i = 0; i < 8; i++) {
                double t = i * 3.14159265359 / 4;
                line(int(8 * cos(t)), int(8 * sin(t)), int(12 * cos(t)), int(12 * sin(t)));
            }
            break;
        case 210:
            line(-10, -3, -10, -10);
            line(-10, -10, -3, -10);
            line(3, -10, 10, -10);
            line(10, -10, 10, -3);
            line(10, 3, 10, 10);
            line(10, 10, 3, 10);
            line(-3, 10, -10, 10);
            line(-10, 10, -10, 3);
            break;
        case 211:
            line(-11, -4, 10, -4);
            line(10, -4, 5, -9);
            line(10, -4, 5, 1);
            line(11, 5, -10, 5);
            line(-10, 5, -5, 0);
            line(-10, 5, -5, 10);
            break;
        case 212:
            box(-12, -8, 12, 9);
            for (int row = 0; row < 2; row++)
                for (int col = 0; col < 5; col++)
                    box(-9 + col * 4, -5 + row * 5, -7 + col * 4, -3 + row * 5);
            line(-5, 6, 6, 6);
            break;
        case 213:
            box(-9, -9, 9, 11);
            box(-4, -12, 4, -6);
            line(-5, -1, 5, -1);
            line(-5, 3, 5, 3);
            line(-5, 7, 3, 7);
            break;
        case 214:
            box(-11, -7, 11, 10);
            line(-10, -10, -3, -10);
            line(-3, -10, 0, -7);
            line(0, 7, 0, -4);
            line(0, -4, -4, 1);
            line(0, -4, 4, 1);
            break;
        case 215:
            line(-9, 9, 8, -8);
            line(-6, 11, 10, -5);
            line(-9, 9, -10, 12);
            line(-10, 12, -6, 11);
            line(5, -9, 11, -3);
            break;
        case 216:
            circle(-5, -5, 4);
            circle(6, -4, 3);
            Arc(d->hDC, cx - S(12), cy, cx + S(3), cy + S(15), cx - S(12), cy + S(8), cx + S(3), cy + S(8));
            Arc(d->hDC, cx + S(1), cy + S(1), cx + S(13), cy + S(15), cx + S(1), cy + S(8), cx + S(13),
                cy + S(8));
            break;
        case 217:
            circle(0, 0, 11);
            line(0, -6, 0, -4);
            line(0, -1, 0, 7);
            break;
        case 218:
            Arc(d->hDC, cx - S(11), cy - S(10), cx + S(11), cy + S(12), cx - S(5), cy - S(9), cx + S(5),
                cy - S(9));
            line(0, -13, 0, 1);
            break;
        default:
            DrawTextW(d->hDC, v.glyph.c_str(), -1, &r, DT_CENTER | DT_VCENTER | DT_SINGLELINE);
        }
    SelectObject(d->hDC, oldBrush);
    SelectObject(d->hDC, oldPen);
    DeleteObject(pen);
    if (d->itemState & ODS_FOCUS) {
        InflateRect(&r, -2, -2);
        DrawFocusRect(d->hDC, &r);
    }
}

static LRESULT themeMessage(HWND h, UINT msg, WPARAM w, LPARAM l) {
    switch (msg) {
    case WM_SETTINGCHANGE: {
        HIGHCONTRASTW state = {sizeof(state)};
        SystemParametersInfoW(SPI_GETHIGHCONTRAST, sizeof(state), &state, 0);
        highContrast = (state.dwFlags & HCF_HIGHCONTRASTON) != 0;
        RedrawWindow(h, nullptr, nullptr, RDW_INVALIDATE | RDW_ERASE | RDW_ALLCHILDREN);
        break;
    }
    case WM_ERASEBKGND: {
        RECT r;
        GetClientRect(h, &r);
        HDC dc = (HDC)w;
        FillRect(dc, &r, highContrast ? GetSysColorBrush(COLOR_WINDOW) : paperBrush);
        wchar_t cls[32] = {};
        GetClassNameW(h, cls, 32);
        if (!highContrast && (h == mainWindow || wcscmp(cls, L"NDDetached") == 0 ||
                              wcscmp(cls, L"NDInbound") == 0)) {
            RECT top = {0, 0, r.right, S(48)};
            FillRect(dc, &top, shellBrush);
            fill(dc, {0, top.bottom - 1, r.right, top.bottom}, edge);
            if (h == mainWindow && selectedPage == 0) {
                fill(dc, {S(24), S(226), std::min(r.right - S(24), long(S(632))), S(380)}, shell);
                RECT input = {S(24), S(154), S(24) + std::min(r.right - S(48), long(S(632))) - S(124), S(194)};
                COLORREF border = GetFocus() == targetEdit ? blue : edge;
                SetDCBrushColor(dc, border);
                FrameRect(dc, &input, (HBRUSH)GetStockObject(DC_BRUSH));
            }
        }
        if (highContrast && h == mainWindow && selectedPage == 0) {
            RECT input = {S(24), S(154), S(24) + std::min(r.right - S(48), long(S(632))) - S(124), S(194)};
            FrameRect(dc, &input, GetSysColorBrush(GetFocus() == targetEdit ? COLOR_HIGHLIGHT : COLOR_WINDOWTEXT));
        }
        if (wcscmp(cls, L"NDViewport") == 0)
            fill(dc, r, highContrast ? GetSysColor(COLOR_WINDOW) : canvasBackground);
        return TRUE;
    }
    case WM_CTLCOLORSTATIC:
    case WM_CTLCOLOREDIT:
    case WM_CTLCOLORLISTBOX:
    case WM_CTLCOLORBTN: {
        HDC dc = (HDC)w;
        HWND child = (HWND)l;
        bool warm = child == category || (h == mainWindow && selectedPage == 0 &&
                     (child == idText || child == statusText || child == idCaption));
        SetBkColor(dc, highContrast ? GetSysColor(COLOR_WINDOW) : warm ? shell : paper);
        SetTextColor(dc, highContrast ? GetSysColor(COLOR_WINDOWTEXT) :
                         child == statusText || child == homeHint || child == inboundText ? muted : ink);
        return (LRESULT)(highContrast ? GetSysColorBrush(COLOR_WINDOW) : warm ? shellBrush : paperBrush);
    }
    case WM_DRAWITEM: {
        auto *item = (DRAWITEMSTRUCT *)l;
        if (item->CtlType == ODT_TAB)
            drawTab(item);
        else if (item->CtlType == ODT_BUTTON)
            drawIcon(item);
        else if (item->CtlType == ODT_LISTBOX)
            drawCategory(item);
        else
            break;
        return TRUE;
    }
    }
    return 0;
}

static HWND hostOf(Remote *r) { return r->detached ? r->detached : mainWindow; }

static bool canvasFocused(Remote *r) {
    return r->hwnd && r->authenticated && !r->ended && IsWindowVisible(r->canvas) &&
           GetForegroundWindow() == hostOf(r) && GetFocus() == r->canvas;
}

static void syncInput() {
    for (auto &entry : remotes) {
        Remote *r = entry.second.get();
        bool active = canvasFocused(r);
        if (r->active == active)
            continue;
        r->active = active;
        if (!active)
            push(24, r->sid);
        push(32, r->sid, "", active);
    }
}

static int tabIndex(Remote *r) {
    auto it = std::find(docked.begin(), docked.end(), r);
    return it == docked.end() ? -1 : int(it - docked.begin()) + 1;
}

static void tabText(HWND bar, int index, const std::wstring &title, bool insert = false) {
    TCITEMW item = {};
    item.mask = TCIF_TEXT;
    item.pszText = const_cast<wchar_t *>(title.c_str());
    SendMessageW(bar, insert ? TCM_INSERTITEMW : TCM_SETITEMW, index, (LPARAM)&item);
}

static std::wstring remoteTitle(Remote *r) {
    return r->targetID + L" · " + r->phase +
           (r->phase == L"已連線" && !r->route.empty() ? L" · " + r->route : L"");
}

static void refreshTab(Remote *r) {
    std::wstring title = remoteTitle(r);
    if (r->detachedTabs)
        tabText(r->detachedTabs, 0, title);
    else if (int index = tabIndex(r); index > 0)
        tabText(tabs, index, title);
}

static void rebuildTabs() {
    TabCtrl_DeleteAllItems(tabs);
    tabText(tabs, 0, L"新增連線", true);
    for (Remote *r : docked)
        tabText(tabs, TabCtrl_GetItemCount(tabs), remoteTitle(r), true);
    if (settingsOpen)
        tabText(tabs, TabCtrl_GetItemCount(tabs), L"設定", true);
    TabCtrl_SetCurSel(tabs, selectedPage == 1 ? int(docked.size()) + 1 :
                           selectedPage == 2 ? tabIndex(selectedRemote) : 0);
}

static void showPage() {
    for (HWND h : homeControls)
        ShowWindow(h, selectedPage == 0 ? SW_SHOW : SW_HIDE);
    for (HWND h : settingsControls)
        ShowWindow(h, selectedPage == 1 ? SW_SHOW : SW_HIDE);
    ShowWindow(settingsPane, selectedPage == 1 ? SW_SHOW : SW_HIDE);
    for (auto f : fields)
        ShowWindow(f.hwnd, f.category == selectedCategory ? SW_SHOW : SW_HIDE);
    ShowWindow(closeTab, selectedPage == 1 ? SW_SHOW : SW_HIDE);
    ShowWindow(closeRemote, selectedPage == 2 && !mainFullscreen ? SW_SHOW : SW_HIDE);
    ShowWindow(tabs, mainFullscreen ? SW_HIDE : SW_SHOW);
    ShowWindow(gear, mainFullscreen ? SW_HIDE : SW_SHOW);
    for (Remote *r : docked)
        ShowWindow(r->hwnd, selectedPage == 2 && selectedRemote == r ? SW_SHOWNA : SW_HIDE);
    layoutHost(mainWindow, selectedPage == 2 ? selectedRemote : nullptr);
    InvalidateRect(mainWindow, nullptr, TRUE);
    syncInput();
}
static void closeSettings() {
    if (!settingsOpen)
        return;
    if (dirty) {
        int choice = MessageBoxW(mainWindow, L"設定尚未儲存。是否儲存變更？\n選擇「否」會捨棄變更。",
                                 L"關閉設定", MB_YESNOCANCEL | MB_ICONQUESTION);
        if (choice == IDCANCEL)
            return;
        if (choice == IDYES) {
            SendMessageW(mainWindow, WM_COMMAND, 1100, 1);
            return;
        }
        push(11);
        dirty = false;
    }
    settingsOpen = false;
    selectedPage = 0;
    rebuildTabs();
    showPage();
}
static void save(bool close) {
    std::string s = "{\"server\":" + escape(text(serverEdit)) + ",\"ca_file\":" + escape(text(caEdit)) +
                    ",\"unattended\":" +
                    (SendMessageW(unattendedCheck, BM_GETCHECK, 0, 0) == BST_CHECKED ? "true" : "false") +
                    ",\"view_password\":" + escape(text(viewEdit)) +
                    ",\"standard_password\":" + escape(text(standardEdit)) +
                    ",\"admin_password\":" + escape(text(adminEdit)) +
                    ",\"upload_gib\":" + utf8(text(limitEdit)) + ",\"clipboard\":" +
                    (SendMessageW(clipboardCheck, BM_GETCHECK, 0, 0) == BST_CHECKED ? "true" : "false") + "}";
    int mode = int(SendMessageW(modeCombo, CB_GETCURSEL, 0, 0));
    s.pop_back();
    s += ",\"relay_only\":" + std::string(mode > 0 ? "true" : "false") +
         ",\"relay_tls_only\":" + std::string(mode == 2 ? "true" : "false") + "}";
    push(10, "", s, close ? 1 : 0);
}
static void settingsField(HWND h, int cat) {
    RECT r;
    GetWindowRect(h, &r);
    MapWindowPoints(HWND_DESKTOP, settingsPane, reinterpret_cast<POINT *>(&r), 2);
    OffsetRect(&r, -S(204), -S(72));
    fields.push_back({h, cat, r});
}

static void layoutSettings() {
    if (!settingsPane || selectedPage != 1)
        return;
    RECT r;
    GetClientRect(settingsPane, &r);
    int content = 0;
    for (const auto &field : fields)
        if (field.category == selectedCategory)
            content = std::max(content, int(field.bounds.bottom));
    settingsScroll = std::clamp(settingsScroll, 0, std::max(0, content - int(r.bottom)));
    SCROLLINFO scroll = {sizeof(scroll), SIF_RANGE | SIF_PAGE | SIF_POS, 0,
                         std::max(0, content - 1), UINT(r.bottom), settingsScroll};
    SetScrollInfo(settingsPane, SB_VERT, &scroll, TRUE);
    GetClientRect(settingsPane, &r);
    for (const auto &field : fields) {
        if (field.category != selectedCategory)
            continue;
        RECT b = field.bounds;
        int width = std::min(int(b.right - b.left), std::max(1, int(r.right - b.left) - S(8)));
        MoveWindow(field.hwnd, b.left, b.top - settingsScroll, width, b.bottom - b.top, TRUE);
    }
}

static LRESULT CALLBACK settingsProc(HWND h, UINT msg, WPARAM w, LPARAM l) {
    if (LRESULT themed = themeMessage(h, msg, w, l))
        return themed;
    switch (msg) {
    case WM_COMMAND:
        return SendMessageW(mainWindow, msg, w, l);
    case WM_MOUSEWHEEL:
        settingsScroll -= GET_WHEEL_DELTA_WPARAM(w) * S(44) / WHEEL_DELTA;
        layoutSettings();
        return 0;
    case WM_VSCROLL: {
        SCROLLINFO scroll = {sizeof(scroll), SIF_TRACKPOS | SIF_PAGE};
        GetScrollInfo(h, SB_VERT, &scroll);
        switch (LOWORD(w)) {
        case SB_LINEUP: settingsScroll -= S(28); break;
        case SB_LINEDOWN: settingsScroll += S(28); break;
        case SB_PAGEUP: settingsScroll -= int(scroll.nPage); break;
        case SB_PAGEDOWN: settingsScroll += int(scroll.nPage); break;
        case SB_THUMBTRACK: settingsScroll = scroll.nTrackPos; break;
        }
        layoutSettings();
        return 0;
    }
    }
    return DefWindowProcW(h, msg, w, l);
}
static void buildMain(HWND h) {
    tabs = control(h, WC_TABCONTROLW, L"", WS_TABSTOP, 100, 12, 8, 540, 32);
    TCITEMW item = {};
    item.mask = TCIF_TEXT;
    item.pszText = (LPWSTR)L"新增連線";
    TabCtrl_InsertItem(tabs, 0, &item);
    gear = icon(h, nullptr, 101, L"⚙", L"設定", 610, 12, 42);
    closeTab = control(h, L"BUTTON", L"關閉設定", WS_TABSTOP, 102, 540, 50, 110, 28);
    closeRemote = control(h, L"BUTTON", L"關閉分頁 ×", WS_TABSTOP, 109, 550, 8, 100, 32);
    connectTitle = label(h, L"連線至遠端電腦", 24, 80, 500, 36);
    SendMessageW(connectTitle, WM_SETFONT, (WPARAM)titleFont, TRUE);
    homeHint = label(h, L"輸入另一台電腦的 NanoDesk ID，開始遠端連線。", 24, 122, 570, 24);
    targetEdit = control(h, L"EDIT", L"", ES_AUTOHSCROLL | WS_TABSTOP, 105, 25, 164, 478, 22);
    SendMessageW(targetEdit, EM_SETCUEBANNER, TRUE, (LPARAM)L"輸入十位數字 ID");
    connectButton = control(h, L"BUTTON", L"連線", WS_TABSTOP | BS_DEFPUSHBUTTON, 106, 516, 154, 112, 40);
    idCaption = label(h, L"本機 ID", 48, 246, 300, 24);
    idText = control(h, L"STATIC", L"尚未取得 ID", SS_NOPREFIX, 103, 48, 276, 350, 42);
    SendMessageW(idText, WM_SETFONT, (WPARAM)titleFont, TRUE);
    copyID = control(h, L"BUTTON", L"複製 ID", WS_TABSTOP, 104, 438, 278, 100, 36);
    EnableWindow(copyID, FALSE);
    statusText = label(h, L"離線", 48, 329, 595, 42);
    SendMessageW(statusText, WM_SETFONT, (WPARAM)smallFont, TRUE);
    inboundText = label(h, L"目前沒有連線", 24, 408, 520, 26);
    SendMessageW(inboundText, WM_SETFONT, (WPARAM)smallFont, TRUE);
    homeControls = {connectTitle, homeHint, targetEdit, connectButton, idCaption, idText, copyID, statusText, inboundText};
    category = control(h, L"LISTBOX", L"", LBS_NOTIFY | WS_TABSTOP, 108, 28, 72, 154, 320);
    for (auto s : {L"一般", L"連線與伺服器", L"安全與授權", L"剪貼簿與檔案", L"關於"})
        SendMessageW(category, LB_ADDSTRING, 0, (LPARAM)s);
    SendMessageW(category, LB_SETCURSEL, 0, 0);
    settingsControls.push_back(category);
    saveSettings = control(h, L"BUTTON", L"儲存設定", WS_TABSTOP, 1100, 470, 418, 140, 36);
    settingsControls.push_back(saveSettings);
    settingsPane = CreateWindowExW(WS_EX_CONTROLPARENT, L"NDSettings", L"", WS_CHILD | WS_VSCROLL |
                                   WS_CLIPCHILDREN, 0, 0, 1, 1, h, nullptr, instance, nullptr);

    settingsField(label(settingsPane,
                        L"NanoDesk 原生 "
                        L"APP\n\n同一程式可控制遠端電腦，也可接受控制。\n關閉遠端視窗會結束該次連線。\n\n無人"
                        L"值守需要本程式持續開啟。",
                        204, 80, 420, 220),
                  0);
    settingsField(label(settingsPane, L"伺服器網址（HTTPS）", 204, 80, 410, 24), 1);
    serverEdit = control(settingsPane, L"EDIT", L"", WS_BORDER | ES_AUTOHSCROLL | WS_TABSTOP, 1201, 204, 112, 410, 30);
    SendMessageW(serverEdit, EM_SETLIMITTEXT, 2048, 0);
    settingsField(serverEdit, 1);
    settingsField(label(settingsPane, L"額外信任的 CA 憑證路徑（選填）", 204, 170, 410, 24), 1);
    caEdit = control(settingsPane, L"EDIT", L"", WS_BORDER | ES_AUTOHSCROLL | WS_TABSTOP, 1202, 204, 202, 302, 30);
    SendMessageW(caEdit, EM_SETLIMITTEXT, 2048, 0);
    settingsField(caEdit, 1);
    settingsField(control(settingsPane, L"BUTTON", L"選擇檔案…", WS_TABSTOP,
                          1210, 514, 202, 100, 30), 1);
    settingsField(label(settingsPane, L"更換伺服器會中斷目前連線。", 204, 254, 410, 32), 1);
    settingsField(label(settingsPane, L"連線模式（排查網路時可切換）", 204, 300, 410, 24), 1);
    modeCombo = control(settingsPane, L"COMBOBOX", L"", CBS_DROPDOWNLIST | WS_TABSTOP, 1209, 204, 334, 410, 150);
    for (auto name : {L"自動：直連優先，中繼備援", L"強制中繼", L"僅 TLS 中繼"})
        SendMessageW(modeCombo, CB_ADDSTRING, 0, (LPARAM)name);
    SendMessageW(modeCombo, CB_SETCURSEL, 0, 0);
    settingsField(modeCombo, 1);
    unattendedCheck =
        control(settingsPane, L"BUTTON", L"啟用無人值守密碼登入", BS_AUTOCHECKBOX | WS_TABSTOP, 1203, 204, 76, 410, 28);
    settingsField(unattendedCheck, 2);
    HWND *passwords[] = {&viewEdit, &standardEdit, &adminEdit};
    const wchar_t *names[] = {L"僅觀看密碼", L"一般控制密碼", L"完全控制密碼"};
    for (int i = 0; i < 3; i++) {
        settingsField(label(settingsPane, names[i], 204, 124 + i * 78, 410, 22), 2);
        *passwords[i] = control(settingsPane, L"EDIT", L"", WS_BORDER | ES_PASSWORD | ES_AUTOHSCROLL | WS_TABSTOP,
                                1204 + i, 204, 150 + i * 78, 410, 28);
        SendMessageW(*passwords[i], EM_SETLIMITTEXT, 64, 0);
        settingsField(*passwords[i], 2);
    }
    settingsField(label(settingsPane, L"空白停用該角色密碼；儲存會撤銷既有授權。", 204, 370, 420, 40), 2);
    clipboardCheck = control(settingsPane, L"BUTTON", L"啟用目前遠端視窗的剪貼簿自動同步", BS_AUTOCHECKBOX | WS_TABSTOP,
                             1207, 204, 84, 420, 30);
    settingsField(clipboardCheck, 3);
    settingsField(label(settingsPane, L"每批傳輸上限（GiB）", 204, 150, 410, 24), 3);
    limitEdit = control(settingsPane, L"EDIT", L"1", WS_BORDER | ES_NUMBER | WS_TABSTOP, 1208, 204, 184, 200, 30);
    settingsField(limitEdit, 3);
    settingsField(label(settingsPane,
                        L"NanoDesk APP\nWindows 10 / 11 x64\n\n原生 Win32 介面、H.264 桌面影像\n固定 "
                        L"ID、直連優先、中繼備援\n\nMIT License",
                        204, 84, 420, 240),
                  4);
    showPage();
}
static void layout(Remote *r) {
    RECT rc;
    GetClientRect(r->hwnd, &rc);
    int w = rc.right, h = rc.bottom;
    int mw = std::max(S(40), w - S(9 * 40 + 34));
    bool overflow = S(int(r->screens.size()) * 40) > mw;
    int header = overflow ? 60 : 40;
    SetWindowPos(r->authLabel, nullptr, S(24), S(110), std::max(1, w - S(48)), S(48), SWP_NOZORDER);
    SetWindowPos(r->request, nullptr, S(24), S(168), S(160), S(36), SWP_NOZORDER);
    SetWindowPos(r->password, nullptr, S(24), S(224), std::max(1, w - S(180)), S(34), SWP_NOZORDER);
    SetWindowPos(r->login, nullptr, w - S(144), S(224), S(120), S(34), SWP_NOZORDER);
    ShowScrollBar(r->monitors, SB_HORZ, overflow);
    SetWindowPos(r->monitors, nullptr, S(8), S(8), mw, S(overflow ? 54 : 38), SWP_NOZORDER);
    SetScrollRange(r->monitors, SB_HORZ, 0, std::max(0, S(int(r->screens.size()) * 40) - mw), FALSE);
    for (size_t i = 0; i < r->screens.size(); i++)
        SetWindowPos(r->screens[i], nullptr, S(int(i) * 40) - r->monitorScroll, 0, S(36), S(34),
                     SWP_NOZORDER);
    for (size_t i = 0; i < r->tools.size(); i++)
        SetWindowPos(r->tools[i], nullptr, i == r->tools.size() - 1 ? w - S(44) : mw + S(16 + int(i) * 40),
                     S(8), S(36), S(34), SWP_NOZORDER);
    SetWindowPos(r->status, nullptr, S(12), S(header + 8), w - S(120), S(24), SWP_NOZORDER);
    SetWindowPos(r->chatButton, nullptr, w - S(96), S(header + 5), S(84), S(28), SWP_NOZORDER);
    SetWindowPos(r->viewport, nullptr, 0, S(header + 36), w, std::max(1, h - S(header + 36)), SWP_NOZORDER);
    RECT vp;
    GetClientRect(r->viewport, &vp);
    int vw = vp.right, vh = vp.bottom, cw = vw, ch = vh;
    int sw = r->sourceWidth ? r->sourceWidth : (r->decoder ? int(r->decoder->width) : 0),
        sh = r->sourceHeight ? r->sourceHeight : (r->decoder ? int(r->decoder->height) : 0);
    if (sw > 0 && sh > 0) {
        double scale = r->zoom ? r->zoom : std::min(double(vw) / sw, double(vh) / sh);
        cw = std::max(1, int(sw * scale));
        ch = std::max(1, int(sh * scale));
    }
    r->scrollX = std::max(0, std::min(r->scrollX, std::max(0, cw - vw)));
    r->scrollY = std::max(0, std::min(r->scrollY, std::max(0, ch - vh)));
    SetScrollRange(r->viewport, SB_HORZ, 0, std::max(0, cw - vw), FALSE);
    SetScrollRange(r->viewport, SB_VERT, 0, std::max(0, ch - vh), FALSE);
    SetScrollPos(r->viewport, SB_HORZ, r->scrollX, TRUE);
    SetScrollPos(r->viewport, SB_VERT, r->scrollY, TRUE);
    SetWindowPos(r->canvas, nullptr, std::max(0, (vw - cw) / 2) - r->scrollX,
                 std::max(0, (vh - ch) / 2) - r->scrollY, cw, ch, SWP_NOZORDER);
}
static void chooseCAFile() {
    wchar_t path[2049] = {};
    OPENFILENAMEW dialog = {};
    dialog.lStructSize = sizeof(dialog);
    dialog.hwndOwner = mainWindow;
    dialog.lpstrFile = path;
    dialog.nMaxFile = DWORD(std::size(path));
    dialog.lpstrTitle = L"選擇匯出的 CA 憑證";
    dialog.lpstrFilter = L"CA 憑證 (*.pem;*.crt;*.cer)\0*.pem;*.crt;*.cer\0所有檔案\0*.*\0\0";
    dialog.Flags = OFN_EXPLORER | OFN_FILEMUSTEXIST | OFN_NOCHANGEDIR;
    if (GetOpenFileNameW(&dialog) && SetWindowTextW(caEdit, path))
        dirty = true;
}

static void chooseFiles(Remote *r) {
    wchar_t paths[32768] = {};
    OPENFILENAMEW o = {};
    o.lStructSize = sizeof(o);
    o.hwndOwner = hostOf(r);
    o.lpstrFile = paths;
    o.nMaxFile = 32768;
    o.lpstrFilter = L"所有檔案\0*.*\0\0";
    o.Flags = OFN_ALLOWMULTISELECT | OFN_EXPLORER | OFN_FILEMUSTEXIST | OFN_NOCHANGEDIR;
    if (!GetOpenFileNameW(&o))
        return;
    std::wstring first = paths;
    wchar_t *p = paths + first.size() + 1;
    std::string result;
    if (!*p)
        result = utf8(first);
    else {
        while (*p) {
            if (!result.empty())
                result += '\n';
            result += utf8(first + L"\\" + p);
            p += wcslen(p) + 1;
        }
    }
    if (result.size() >= 8192) {
        ND_Error("選取的檔案路徑過長，請分批傳送");
        return;
    }
    push(27, r->sid, result);
}
static void menu(Remote *r, int id, HWND button) {
    HMENU m = CreatePopupMenu();
    if (id == 210) {
        AppendMenuW(m, MF_STRING, 1, L"適應視窗");
        AppendMenuW(m, MF_STRING, 2, L"原始大小 100%");
        AppendMenuW(m, MF_STRING, 3, L"50%");
        AppendMenuW(m, MF_STRING, 4, L"150%");
        AppendMenuW(m, MF_STRING, 5, L"200%");
        AppendMenuW(m, MF_SEPARATOR, 0, nullptr);
        AppendMenuW(m, MF_STRING, 6, L"全螢幕 / 還原");
    }
    if (id == 211) {
        AppendMenuW(m, MF_STRING, 1, L"取得／接管主控權");
        AppendMenuW(m, MF_STRING, 2, L"釋放主控權");
    }
    if (id == 212) {
        AppendMenuW(m, MF_STRING, 1, L"Windows 鍵");
        AppendMenuW(m, MF_STRING, 2, L"Alt + Tab");
        AppendMenuW(m, MF_STRING, 3, L"顯示桌面");
        AppendMenuW(m, MF_STRING | (r->role < 3 ? MF_GRAYED : 0), 4, L"工作管理員");
    }
    if (id == 213) {
        AppendMenuW(m, MF_STRING | (r->clip ? MF_CHECKED : 0), 1, L"自動同步剪貼簿");
        AppendMenuW(m, MF_STRING, 2, L"貼上本機文字／圖片");
    }
    if (id == 214) {
        AppendMenuW(m, MF_STRING, 1, L"傳送檔案");
        AppendMenuW(m, MF_STRING, 2, L"取消傳輸");
    }
    RECT rc;
    GetWindowRect(button, &rc);
    int choice = TrackPopupMenu(m, TPM_RETURNCMD | TPM_LEFTALIGN, rc.left, rc.bottom, 0, r->hwnd, nullptr);
    DestroyMenu(m);
    if (!choice)
        return;
    if (id == 210) {
        if (choice == 6) {
            fullscreen(r);
        } else {
            double scales[] = {0, 0, 1, .5, 1.5, 2};
            r->zoom = scales[choice];
            layout(r);
        }
    }
    if (id == 211)
        command(r, choice == 1 ? "{\"type\":\"take_control\"}" : "{\"type\":\"release_control\"}");
    if (id == 212) {
        const char *keys[] = {"", "win", "alt_tab", "win_d", "ctrl_shift_esc"};
        command(r, std::string("{\"type\":\"special_key\",\"key\":\"") + keys[choice] + "\"}");
    }
    if (id == 213) {
        if (choice == 1) {
            r->clip = !r->clip;
            updateTip(r, button, r->clip ? L"剪貼簿同步：開啟" : L"剪貼簿同步：關閉");
            push(29, r->sid, "", r->clip);
            icons[button].selected = r->clip;
            InvalidateRect(button, nullptr, TRUE);
        } else
            push(30, r->sid);
    }
    if (id == 214) {
        if (choice == 1)
            chooseFiles(r);
        else
            push(31, r->sid);
    }
}
static LRESULT CALLBACK canvasProc(HWND h, UINT msg, WPARAM w, LPARAM l) {
    Remote *r = remote(h);
    if (!r)
        return DefWindowProcW(h, msg, w, l);
    switch (msg) {
    case WM_PAINT: {
        PAINTSTRUCT ps;
        HDC dc = BeginPaint(h, &ps);
        RECT rc;
        GetClientRect(h, &rc);
        fill(dc, rc, highContrast ? GetSysColor(COLOR_WINDOW) : canvasBackground);
        if (r->decoder)
            r->decoder->paint(dc, rc.right, rc.bottom);
        for (auto &item : pointers[r->sid]) {
            auto &p = item.second;
            if (GetTickCount64() - p.at > 1500)
                continue;
            int x = int(p.x * rc.right), y = int(p.y * rc.bottom);
            HBRUSH brush = CreateSolidBrush(p.color);
            HBRUSH old = (HBRUSH)SelectObject(dc, brush);
            Ellipse(dc, x - 5, y - 5, x + 5, y + 5);
            SelectObject(dc, old);
            DeleteObject(brush);
            SetBkMode(dc, TRANSPARENT);
            SetTextColor(dc, p.color);
            TextOutW(dc, x + 8, y - 10, p.name.c_str(), int(p.name.size()));
        }
        EndPaint(h, &ps);
        return 0;
    }
    case WM_ERASEBKGND:
        return 1;
    case WM_GETDLGCODE:
        return DLGC_WANTALLKEYS | DLGC_WANTARROWS | DLGC_WANTTAB;
    case WM_MOUSEMOVE:
    case WM_LBUTTONDOWN:
    case WM_LBUTTONUP:
    case WM_RBUTTONDOWN:
    case WM_RBUTTONUP:
    case WM_MBUTTONDOWN:
    case WM_MBUTTONUP: {
        if (!r->authenticated || r->ended || !IsWindowVisible(h) ||
            GetForegroundWindow() != hostOf(r))
            return 0;
        if (msg != WM_LBUTTONDOWN && msg != WM_RBUTTONDOWN && msg != WM_MBUTTONDOWN &&
            !canvasFocused(r))
            return 0;
        RECT rc;
        GetClientRect(h, &rc);
        double x = std::max(0., std::min(1., double(GET_X_LPARAM(l)) / std::max(1L, rc.right))),
               y = std::max(0., std::min(1., double(GET_Y_LPARAM(l)) / std::max(1L, rc.bottom)));
        if (msg == WM_MOUSEMOVE)
            push(20, r->sid, "", r->laser ? 1 : 0, 0, x, y);
        else {
            bool down = msg == WM_LBUTTONDOWN || msg == WM_RBUTTONDOWN || msg == WM_MBUTTONDOWN;
            int button = (msg == WM_RBUTTONDOWN || msg == WM_RBUTTONUP)   ? 2
                         : (msg == WM_MBUTTONDOWN || msg == WM_MBUTTONUP) ? 1
                                                                          : 0;
            unsigned mask = 1u << button;
            if (down) {
                SetFocus(h);
                if (!canvasFocused(r))
                    return 0;
                SetCapture(h);
                r->buttonsDown |= mask;
            } else {
                if (!(r->buttonsDown & mask))
                    return 0;
                r->buttonsDown &= ~mask;
            }
            push(20, r->sid, "", 0, 0, x, y);
            push(21, r->sid, "", button, down ? 1 : 0);
            if (!r->buttonsDown && GetCapture() == h)
                ReleaseCapture();
        }
        return 0;
    }
    case WM_MOUSEWHEEL:
        if (canvasFocused(r))
            push(22, r->sid, "", 0, -GET_WHEEL_DELTA_WPARAM(w));
        return 0;
    case WM_MOUSEHWHEEL:
        if (canvasFocused(r))
            push(22, r->sid, "", GET_WHEEL_DELTA_WPARAM(w), 0);
        return 0;
    case WM_KEYDOWN:
    case WM_KEYUP:
    case WM_SYSKEYDOWN:
    case WM_SYSKEYUP:
        if (canvasFocused(r))
            push(23, r->sid, "", int(w), msg == WM_KEYDOWN || msg == WM_SYSKEYDOWN ? 1 : 0,
                 double((l >> 24) & 1), double((l >> 16) & 255));
        return 0;
    case WM_KILLFOCUS:
        if (GetCapture() == h)
            ReleaseCapture();
        syncInput();
        return 0;
    case WM_CAPTURECHANGED:
        if (r->buttonsDown) {
            r->buttonsDown = 0;
            push(24, r->sid);
        }
        return 0;
    }
    return DefWindowProcW(h, msg, w, l);
}
static LRESULT CALLBACK viewportProc(HWND h, UINT msg, WPARAM w, LPARAM l) {
    if (LRESULT themed = themeMessage(h, msg, w, l))
        return themed;
    Remote *r = remote(h);
    if (r && (msg == WM_HSCROLL || msg == WM_VSCROLL)) {
        int &pos = msg == WM_HSCROLL ? r->scrollX : r->scrollY;
        int action = LOWORD(w);
        if (action == SB_THUMBTRACK)
            pos = HIWORD(w);
        else if (action == SB_LINELEFT)
            pos -= 40;
        else if (action == SB_LINERIGHT)
            pos += 40;
        else if (action == SB_PAGELEFT)
            pos -= 300;
        else if (action == SB_PAGERIGHT)
            pos += 300;
        layout(r);
        return 0;
    }
    return DefWindowProcW(h, msg, w, l);
}
static LRESULT CALLBACK monitorProc(HWND h, UINT msg, WPARAM w, LPARAM l) {
    if (LRESULT themed = themeMessage(h, msg, w, l))
        return themed;
    Remote *r = remote(h);
    if (r) {
        if (msg == WM_DRAWITEM) {
            drawIcon((DRAWITEMSTRUCT *)l);
            return TRUE;
        }
        if (msg == WM_COMMAND) {
            int index = LOWORD(w) - 1000;
            command(r, "{\"type\":\"switch_monitor\",\"index\":" + std::to_string(index) + "}");
            return 0;
        }
        if (msg == WM_HSCROLL) {
            int action = LOWORD(w);
            if (action == SB_THUMBTRACK)
                r->monitorScroll = HIWORD(w);
            else if (action == SB_LINELEFT)
                r->monitorScroll -= 40;
            else if (action == SB_LINERIGHT)
                r->monitorScroll += 40;
            else if (action == SB_PAGELEFT)
                r->monitorScroll -= 160;
            else if (action == SB_PAGERIGHT)
                r->monitorScroll += 160;
            r->monitorScroll = std::max(0, r->monitorScroll);
            layout(r);
            return 0;
        }
    }
    return DefWindowProcW(h, msg, w, l);
}
static LRESULT CALLBACK panelProc(HWND h, UINT msg, WPARAM w, LPARAM l) {
    if (LRESULT themed = themeMessage(h, msg, w, l))
        return themed;
    if (msg == WM_SIZE) {
        RECT rc;
        GetClientRect(h, &rc);
        HWND edit = GetDlgItem(h, 400);
        if (edit)
            SetWindowPos(edit, nullptr, S(12), S(12), std::max(S(100), int(rc.right) - S(24)),
                         std::max(S(60), int(rc.bottom) - S(24)), SWP_NOZORDER);
        return 0;
    }
    return DefWindowProcW(h, msg, w, l);
}
static std::wstring connectionDetails(Remote *r) {
    const wchar_t *role = r->role == 3   ? L"完全控制"
                          : r->role == 2 ? L"一般控制"
                          : r->role == 1 ? L"僅觀看"
                                         : L"未授權";
    return L"遠端 ID：" + r->targetID + L"\r\n" + r->info + L"\r\n角色：" + role + L"\r\n螢幕數量：" +
           std::to_wstring(r->screens.size());
}
static void openPanel(Remote *r, bool members) {
    HWND &h = members ? r->membersPanel : r->infoPanel;
    if (IsWindow(h)) {
        SetForegroundWindow(h);
        return;
    }
    h = CreateWindowExW(WS_EX_TOOLWINDOW, L"NDPanel", members ? L"連線成員" : L"連線資訊",
                        WS_OVERLAPPEDWINDOW | WS_CLIPCHILDREN, CW_USEDEFAULT, CW_USEDEFAULT, S(460), S(300),
                        r->hwnd, nullptr, instance, nullptr);
    std::wstring content = members ? r->members : connectionDetails(r);
    control(h, L"EDIT", content.c_str(),
            WS_BORDER | ES_MULTILINE | ES_READONLY | ES_AUTOVSCROLL | WS_VSCROLL | WS_TABSTOP, 400, 12, 12,
            420, 230);
    ShowWindow(h, SW_SHOW);
}

static LRESULT CALLBACK remoteProc(HWND h, UINT msg, WPARAM w, LPARAM l) {
    if (LRESULT themed = themeMessage(h, msg, w, l))
        return themed;
    Remote *r = remote(h);
    if (!r)
        return DefWindowProcW(h, msg, w, l);
    switch (msg) {
    case WM_GETMINMAXINFO:
        ((MINMAXINFO *)l)->ptMinTrackSize = {S(540), S(400)};
        return 0;
    case WM_SIZE:
        if (r->canvas)
            layout(r);
        return 0;
    case WM_DRAWITEM:
        drawIcon((DRAWITEMSTRUCT *)l);
        return TRUE;
    case WM_SETFOCUS:
        if (r->authenticated)
            SetFocus(r->canvas);
        return 0;
    case WM_TIMER:
        if (IsWindowVisible(h) && r->authenticated && !r->ended && GetTickCount64() - r->lastFrame > 4000) {
            SetWindowTextW(r->status, L"畫面停滯，正在要求關鍵幀…");
            r->noticeUntil = GetTickCount64();
            if (GetTickCount64() - r->lastKeyRequest > 2000) {
                command(r, "{\"type\":\"request_keyframe\"}");
                r->lastKeyRequest = GetTickCount64();
            }
        }
        return 0;
    case WM_COMMAND: {
        int id = LOWORD(w);
        if (id == 201) {
            command(r, "{\"type\":\"auth_request\"}");
            EnableWindow(r->request, FALSE);
            icons[r->tools[7]].state = 1;
            InvalidateRect(r->tools[7], nullptr, TRUE);
            SetWindowTextW(r->status, L"等待被控端核准（60 秒）");
            r->phase = L"等待授權";
            refreshTab(r);
        } else if (id == 202) {
            push(26, r->sid, utf8(text(r->password)));
            SetWindowTextW(r->password, L"");
        } else if (id >= 210 && id <= 214)
            menu(r, id, (HWND)l);
        else if (id == 215) {
            r->laser = !r->laser;
            icons[(HWND)l].selected = r->laser;
            InvalidateRect((HWND)l, nullptr, TRUE);
        } else if (id == 216)
            openPanel(r, true);
        else if (id == 217)
            openPanel(r, false);
        else if (id == 218)
            SendMessageW(h, WM_CLOSE, 0, 0);
        else if (id == 219)
            openChat(r);
        return 0;
    }
    case WM_CLOSE:
        if (r->fullscreen)
            fullscreen(r);
        push(28, r->sid);
        if (r->detached) {
            DestroyWindow(r->detached);
        } else {
            docked.erase(std::remove(docked.begin(), docked.end(), r), docked.end());
            if (selectedRemote == r) {
                selectedRemote = nullptr;
                selectedPage = 0;
            }
            DestroyWindow(h);
            rebuildTabs();
            showPage();
        }
        return 0;
    case WM_DESTROY:
        for (HWND tool : r->tools)
            icons.erase(tool);
        for (HWND screen : r->screens)
            icons.erase(screen);
        for (HWND panel : {r->infoPanel, r->membersPanel, r->chatWindow, r->tooltip})
            if (IsWindow(panel))
                DestroyWindow(panel);
        KillTimer(h, 1);
        r->ended = true;
        r->hwnd = nullptr;
        r->decoder.reset();
        return 0;
    }
    return DefWindowProcW(h, msg, w, l);
}
static LRESULT CALLBACK approvalProc(HWND h, UINT msg, WPARAM w, LPARAM l) {
    if (LRESULT themed = themeMessage(h, msg, w, l))
        return themed;
    auto it = approvals.find(h);
    if (it != approvals.end()) {
        if (msg == WM_COMMAND) {
            auto a = it->second;
            push(40, a.sid, a.request, LOWORD(w) >= 301 && LOWORD(w) <= 303 ? LOWORD(w) - 300 : 0);
            DestroyWindow(h);
            return 0;
        }
        if (msg == WM_CLOSE) {
            auto a = it->second;
            push(40, a.sid, a.request, 0);
            DestroyWindow(h);
            return 0;
        }
        if (msg == WM_TIMER && GetTickCount64() - it->second.start >= 60000) {
            SendMessageW(h, WM_CLOSE, 0, 0);
            return 0;
        }
        if (msg == WM_DESTROY) {
            KillTimer(h, 1);
            approvals.erase(it);
            return 0;
        }
    }
    return DefWindowProcW(h, msg, w, l);
}
static void layoutHost(HWND host, Remote *r) {
    RECT rc;
    GetClientRect(host, &rc);
    bool main = host == mainWindow;
    int w = rc.right, top = r && r->fullscreen ? 0 : S(48);
    if (main) {
        int content = std::min(w - S(48), S(632));
        MoveWindow(targetEdit, S(24) + 1, S(165), std::max(1, content - S(124) - 2), S(24), TRUE);
        MoveWindow(connectButton, S(24) + content - S(112), S(154), S(112), S(40), TRUE);
        MoveWindow(copyID, S(24) + content - S(124), S(278), S(100), S(36), TRUE);
        MoveWindow(statusText, S(48), S(329), std::max(1, content - S(48)), S(42), TRUE);
        MoveWindow(closeTab, w - S(136), rc.bottom - S(52), S(112), S(36), TRUE);
        MoveWindow(saveSettings, w - S(288), rc.bottom - S(52), S(140), S(36), TRUE);
        MoveWindow(category, S(24), S(72), S(156), std::max(1, int(rc.bottom) - S(148)), TRUE);
        MoveWindow(settingsPane, S(204), S(72), std::max(1, w - S(228)),
                   std::max(1, int(rc.bottom) - S(144)), TRUE);
        layoutSettings();
        int reserve = selectedPage == 2 ? 168 : 62;
        MoveWindow(tabs, S(12), S(8), std::max(1, w - S(reserve)), S(32), TRUE);
        MoveWindow(gear, w - S(48), S(8), S(36), S(32), TRUE);
        MoveWindow(closeRemote, w - S(154), S(8), S(98), S(32), TRUE);
    } else if (r && r->detachedTabs) {
        MoveWindow(r->detachedTabs, S(12), S(8), std::max(1, w - S(24)), S(32), TRUE);
    }
    if (r && r->hwnd)
        MoveWindow(r->hwnd, 0, top, w, std::max(1, int(rc.bottom) - top), TRUE);
}

static void fullscreen(Remote *r) {
    HWND host = hostOf(r);
    if (!r->fullscreen) {
        GetWindowRect(host, &r->restore);
        r->style = GetWindowLongPtrW(host, GWL_STYLE);
        MONITORINFO mi = {sizeof(mi)};
        GetMonitorInfoW(MonitorFromWindow(host, MONITOR_DEFAULTTONEAREST), &mi);
        r->fullscreen = true;
        if (!r->detached)
            mainFullscreen = r;
        SetWindowLongPtrW(host, GWL_STYLE, r->style & ~WS_OVERLAPPEDWINDOW);
        SetWindowPos(host, nullptr, mi.rcMonitor.left, mi.rcMonitor.top,
                     mi.rcMonitor.right - mi.rcMonitor.left, mi.rcMonitor.bottom - mi.rcMonitor.top,
                     SWP_FRAMECHANGED | SWP_NOZORDER);
    } else {
        r->fullscreen = false;
        if (mainFullscreen == r)
            mainFullscreen = nullptr;
        SetWindowLongPtrW(host, GWL_STYLE, r->style);
        SetWindowPos(host, nullptr, r->restore.left, r->restore.top,
                     r->restore.right - r->restore.left, r->restore.bottom - r->restore.top,
                     SWP_FRAMECHANGED | SWP_NOZORDER);
    }
    if (r->detached) {
        ShowWindow(r->detachedTabs, r->fullscreen ? SW_HIDE : SW_SHOWNA);
        layoutHost(host, r);
    } else {
        showPage();
    }
}

static void dockRemote(Remote *r, int index) {
    HWND oldHost = r->detached;
    if (!oldHost || r->fullscreen || mainFullscreen)
        return;
    ShowWindow(r->hwnd, SW_HIDE);
    syncInput();
    if (!SetParent(r->hwnd, mainWindow)) {
        ShowWindow(r->hwnd, SW_SHOWNA);
        return;
    }
    r->detached = r->detachedTabs = nullptr;
    DestroyWindow(oldHost);
    docked.insert(docked.begin() + std::clamp(index, 0, int(docked.size())), r);
    selectedPage = 2;
    selectedRemote = r;
    rebuildTabs();
    showPage();
    SetForegroundWindow(mainWindow);
    if (r->authenticated && !r->ended)
        SetFocus(r->canvas);
}

static LRESULT CALLBACK detachedProc(HWND h, UINT msg, WPARAM w, LPARAM l) {
    if (LRESULT themed = themeMessage(h, msg, w, l))
        return themed;
    if (msg == WM_NCCREATE)
        SetWindowLongPtrW(h, GWLP_USERDATA, (LONG_PTR)((CREATESTRUCTW *)l)->lpCreateParams);
    Remote *r = remote(h);
    if (r && r->detached == h) {
        switch (msg) {
        case WM_SIZE:
            layoutHost(h, r);
            return 0;
        case WM_GETMINMAXINFO:
            ((MINMAXINFO *)l)->ptMinTrackSize = {S(560), S(420)};
            return 0;
        case WM_SYSCOMMAND:
            if ((w & 0xfff0) == 0x1ff0) {
                dockRemote(r, int(docked.size()));
                return 0;
            }
            break;
        case WM_CLOSE:
            SendMessageW(r->hwnd, WM_CLOSE, 0, 0);
            return 0;
        }
    }
    return DefWindowProcW(h, msg, w, l);
}

static LRESULT CALLBACK tabProc(HWND, UINT, WPARAM, LPARAM, UINT_PTR, DWORD_PTR);

static void detachRemote(Remote *r, POINT at) {
    if (r->detached || r->fullscreen)
        return;
    HWND host = CreateWindowExW(0, L"NDDetached", (L"NanoDesk — " + r->targetID).c_str(),
                                WS_OVERLAPPEDWINDOW | WS_CLIPCHILDREN, at.x - S(40), at.y - S(48),
                                S(1000), S(720), nullptr, nullptr, instance, r);
    if (!host)
        return;
    RECT bounds;
    GetWindowRect(host, &bounds);
    MONITORINFO mi = {sizeof(mi)};
    if (GetMonitorInfoW(MonitorFromPoint(at, MONITOR_DEFAULTTONEAREST), &mi)) {
        int width = std::min(bounds.right - bounds.left, mi.rcWork.right - mi.rcWork.left);
        int height = std::min(bounds.bottom - bounds.top, mi.rcWork.bottom - mi.rcWork.top);
        MoveWindow(host, std::clamp(int(bounds.left), int(mi.rcWork.left), int(mi.rcWork.right) - width),
                   std::clamp(int(bounds.top), int(mi.rcWork.top), int(mi.rcWork.bottom) - height),
                   width, height, FALSE);
    }
    ShowWindow(r->hwnd, SW_HIDE);
    syncInput();
    if (!SetParent(r->hwnd, host)) {
        DestroyWindow(host);
        showPage();
        return;
    }
    r->detached = host;
    HMENU systemMenu = GetSystemMenu(host, FALSE);
    InsertMenuW(systemMenu, 0, MF_BYPOSITION | MF_STRING, 0x1ff0, L"移回主視窗");
    r->detachedTabs = control(host, WC_TABCONTROLW, L"", WS_TABSTOP, 100, 12, 8, 500, 32);
    tabText(r->detachedTabs, 0, remoteTitle(r), true);
    SetWindowSubclass(r->detachedTabs, tabProc, 1, (DWORD_PTR)r);
    docked.erase(docked.begin() + tabIndex(r) - 1);
    if (selectedRemote == r) {
        selectedRemote = nullptr;
        selectedPage = 0;
    }
    rebuildTabs();
    showPage();
    layoutHost(host, r);
    ShowWindow(r->hwnd, SW_SHOWNA);
    ShowWindow(host, SW_SHOW);
    if (r->authenticated && !r->ended)
        SetFocus(r->canvas);
}

static HWND dragTab;
static Remote *dragRemote;
static POINT dragStart;

static int hitTab(HWND bar, POINT at) {
    ScreenToClient(bar, &at);
    TCHITTESTINFO hit = {at, 0};
    return TabCtrl_HitTest(bar, &hit);
}

static LRESULT CALLBACK tabProc(HWND h, UINT msg, WPARAM w, LPARAM l, UINT_PTR, DWORD_PTR data) {
    Remote *floating = (Remote *)data;
    if (msg == WM_LBUTTONDOWN) {
        POINT at = {GET_X_LPARAM(l), GET_Y_LPARAM(l)};
        ClientToScreen(h, &at);
        int index = hitTab(h, at);
        Remote *r = floating ? (index == 0 ? floating : nullptr) :
                    index > 0 && index <= int(docked.size()) ? docked[index - 1] : nullptr;
        if (r) {
            dragTab = h;
            dragRemote = r;
            dragStart = at;
            SetFocus(h);
        }
        LRESULT result = DefSubclassProc(h, msg, w, l);
        if (r)
            SetCapture(h);
        return result;
    }
    if ((msg == WM_CAPTURECHANGED || msg == WM_NCDESTROY) && dragTab == h) {
        dragTab = nullptr;
        dragRemote = nullptr;
    }
    if (msg == WM_LBUTTONUP && dragTab == h) {
        Remote *r = dragRemote;
        POINT at = {GET_X_LPARAM(l), GET_Y_LPARAM(l)};
        ClientToScreen(h, &at);
        bool moved = std::abs(at.x - dragStart.x) >= GetSystemMetrics(SM_CXDRAG) ||
                     std::abs(at.y - dragStart.y) >= GetSystemMetrics(SM_CYDRAG);
        ReleaseCapture();
        if (!moved || !r || !r->hwnd) {
            LRESULT result = DefSubclassProc(h, msg, w, l);
            if (r && r->authenticated && !r->ended && IsWindowVisible(r->hwnd))
                SetFocus(r->canvas);
            return result;
        }
        if (r->ended)
            return 0;
        RECT bar, window;
        GetWindowRect(tabs, &bar);
        GetWindowRect(mainWindow, &window);
        if (!mainFullscreen && PtInRect(&bar, at)) {
            int index = hitTab(tabs, at);
            if (floating)
                dockRemote(r, index < 0 ? int(docked.size()) : index - 1);
            else if (index > 0 && index <= int(docked.size())) {
                docked.erase(docked.begin() + tabIndex(r) - 1);
                docked.insert(docked.begin() + index - 1, r);
                rebuildTabs();
                showPage();
            }
        } else if (!floating && !PtInRect(&window, at)) {
            detachRemote(r, at);
        }
        return 0;
    }
    if (msg == WM_CONTEXTMENU && floating) {
        POINT at = {GET_X_LPARAM(l), GET_Y_LPARAM(l)};
        if (at.x == -1 && at.y == -1) {
            at = {0, S(30)};
            ClientToScreen(h, &at);
        }
        HMENU menu = CreatePopupMenu();
        AppendMenuW(menu, mainFullscreen ? MF_GRAYED : MF_STRING, 1, L"移回主視窗");
        int choice = TrackPopupMenu(menu, TPM_RETURNCMD, at.x, at.y, 0, floating->detached, nullptr);
        DestroyMenu(menu);
        if (choice == 1)
            dockRemote(floating, int(docked.size()));
        return 0;
    }
    return DefSubclassProc(h, msg, w, l);
}

struct Inbound {
    std::string sid;
    std::wstring device, role;
    bool controller;
};

struct ChatControls {
    HWND history = nullptr, input = nullptr, send = nullptr;
};

static HWND inboundWindow, inboundTabs, inboundDetails, inboundStop;
static ChatControls inboundChat;
static std::vector<Inbound> inboundPeers, incomingPeers;
static std::string selectedInbound;
static std::map<std::string, std::deque<std::wstring>> chats;
static std::map<std::string, std::wstring> inboundDrafts;

static void buildChat(HWND owner, ChatControls &chat) {
    chat.history = control(owner, L"EDIT", L"", WS_BORDER | ES_MULTILINE | ES_READONLY |
                           ES_AUTOVSCROLL | WS_VSCROLL | WS_TABSTOP, 501, 0, 0, 1, 1);
    SendMessageW(chat.history, EM_SETLIMITTEXT, 150000, 0);
    chat.input = control(owner, L"EDIT", L"", WS_BORDER | ES_AUTOHSCROLL | WS_TABSTOP,
                         502, 0, 0, 1, 1);
    SendMessageW(chat.input, EM_SETLIMITTEXT, 2048, 0);
    SendMessageW(chat.input, EM_SETCUEBANNER, 0, (LPARAM)L"輸入訊息");
    chat.send = control(owner, L"BUTTON", L"傳送", WS_TABSTOP, 503, 0, 0, 1, 1);
}

static void layoutChat(ChatControls &chat, int left, int top, int right, int bottom) {
    int inputY = bottom - S(32);
    MoveWindow(chat.history, left, top, right - left, inputY - top - S(8), TRUE);
    MoveWindow(chat.input, left, inputY, right - left - S(76), S(32), TRUE);
    MoveWindow(chat.send, right - S(68), inputY, S(68), S(32), TRUE);
}

static void renderChat(const ChatControls &chat, const std::string &sid) {
    std::wstring history;
    auto it = chats.find(sid);
    if (it != chats.end())
        for (const auto &line : it->second)
            history += line + L"\r\n";
    if (history.empty())
        history = L"已建立連線。";
    SetWindowTextW(chat.history, history.c_str());
    SendMessageW(chat.history, EM_SETSEL, history.size(), history.size());
    SendMessageW(chat.history, EM_SCROLLCARET, 0, 0);
}

static void sendChat(const ChatControls &chat, const std::string &sid) {
    std::string message = utf8(text(chat.input));
    if (sid.empty() || message.empty())
        return;
    push(41, sid, message);
    SetWindowTextW(chat.input, L"");
    SetFocus(chat.input);
}

static void renderInbound(bool changeChat) {
    auto it = std::find_if(inboundPeers.begin(), inboundPeers.end(), [](const Inbound &peer) {
        return peer.sid == selectedInbound;
    });
    if (it == inboundPeers.end())
        return;
    std::wstring details = L"裝置 " + it->device + L"\r\n\r\n授權角色\r\n" + it->role;
    details += it->controller ? L"\r\n\r\n目前持有主控權" : L"\r\n\r\n目前未持有主控權";
    SetWindowTextW(inboundDetails, details.c_str());
    if (changeChat) {
        renderChat(inboundChat, selectedInbound);
        auto draft = inboundDrafts.find(selectedInbound);
        SetWindowTextW(inboundChat.input, draft == inboundDrafts.end() ? L"" : draft->second.c_str());
    }
}

static LRESULT CALLBACK inboundProc(HWND h, UINT msg, WPARAM w, LPARAM l) {
    if (LRESULT themed = themeMessage(h, msg, w, l))
        return themed;
    switch (msg) {
    case WM_CREATE:
        inboundTabs = control(h, WC_TABCONTROLW, L"", WS_TABSTOP, 500, 0, 0, 1, 1);
        inboundDetails = label(h, L"", 24, 68, 182, 134);
        inboundStop = control(h, L"BUTTON", L"結束此連線", WS_TABSTOP, 504, 0, 0, 1, 1);
        buildChat(h, inboundChat);
        return 0;
    case WM_GETMINMAXINFO:
        ((MINMAXINFO *)l)->ptMinTrackSize = {S(590), S(330)};
        return 0;
    case WM_SIZE: {
        RECT rc;
        GetClientRect(h, &rc);
        MoveWindow(inboundTabs, S(24), S(8), rc.right - S(48), S(36), TRUE);
        MoveWindow(inboundStop, S(24), rc.bottom - S(56), S(180), S(32), TRUE);
        layoutChat(inboundChat, S(224), S(64), rc.right - S(24), rc.bottom - S(24));
        return 0;
    }
    case WM_NOTIFY:
        if (((NMHDR *)l)->hwndFrom == inboundTabs && ((NMHDR *)l)->code == TCN_SELCHANGE) {
            int index = TabCtrl_GetCurSel(inboundTabs);
            if (index >= 0 && index < int(inboundPeers.size())) {
                inboundDrafts[selectedInbound] = text(inboundChat.input);
                selectedInbound = inboundPeers[index].sid;
                renderInbound(true);
            }
        }
        return 0;
    case WM_COMMAND:
        if (LOWORD(w) == 503)
            sendChat(inboundChat, selectedInbound);
        else if (LOWORD(w) == 504 && !selectedInbound.empty())
            push(42, selectedInbound);
        return 0;
    case WM_CLOSE:
        if (!inboundPeers.empty() &&
            MessageBoxW(h, L"確定結束所有連入的遠端工作階段？", L"結束連入",
                        MB_YESNO | MB_DEFBUTTON2 | MB_ICONWARNING) == IDYES) {
            for (const Inbound &peer : inboundPeers)
                push(42, peer.sid);
        }
        return 0;
    }
    return DefWindowProcW(h, msg, w, l);
}

static LRESULT CALLBACK chatProc(HWND h, UINT msg, WPARAM w, LPARAM l) {
    if (LRESULT themed = themeMessage(h, msg, w, l))
        return themed;
    if (msg == WM_NCCREATE)
        SetWindowLongPtrW(h, GWLP_USERDATA, (LONG_PTR)((CREATESTRUCTW *)l)->lpCreateParams);
    Remote *r = remote(h);
    if (msg == WM_CREATE) {
        ChatControls chat;
        buildChat(h, chat);
        renderChat(chat, r->sid);
        return 0;
    }
    if (msg == WM_SIZE || msg == WM_COMMAND) {
        ChatControls chat = {GetDlgItem(h, 501), GetDlgItem(h, 502), GetDlgItem(h, 503)};
        if (msg == WM_SIZE && w != SIZE_MINIMIZED) {
            RECT rc;
            GetClientRect(h, &rc);
            layoutChat(chat, S(12), S(12), rc.right - S(12), rc.bottom - S(12));
        } else if (msg == WM_COMMAND && LOWORD(w) == 503) {
            sendChat(chat, r->sid);
        }
        return 0;
    }
    if (msg == WM_DESTROY)
        r->chatWindow = nullptr;
    return DefWindowProcW(h, msg, w, l);
}

static void openChat(Remote *r) {
    if (!r->authenticated || r->ended)
        return;
    if (!r->chatWindow)
        r->chatWindow = CreateWindowExW(WS_EX_TOOLWINDOW, L"NDChat",
                            (L"對話 — " + r->targetID).c_str(),
                            WS_OVERLAPPED | WS_CAPTION | WS_SYSMENU | WS_CLIPCHILDREN,
                            CW_USEDEFAULT, CW_USEDEFAULT, S(450), S(330), mainWindow,
                            nullptr, instance, r);
    if (r->chatWindow) {
        SetWindowTextW(r->chatButton, L"對話");
        ShowWindow(r->chatWindow, SW_SHOW);
        SetForegroundWindow(r->chatWindow);
        SetFocus(GetDlgItem(r->chatWindow, 502));
    }
}

extern "C" void ND_InboundBegin() { incomingPeers.clear(); }

extern "C" void ND_InboundAdd(const char *sid, const char *device, const char *role, int controller) {
    incomingPeers.push_back({sid, wide(device), wide(role), controller != 0});
}

extern "C" void ND_InboundEnd() {
    auto oldSelection = selectedInbound;
    bool tabsChanged = incomingPeers.size() != inboundPeers.size();
    for (size_t i = 0; i < inboundPeers.size(); ++i) {
        const auto &peer = inboundPeers[i];
        if (i >= incomingPeers.size() || incomingPeers[i].sid != peer.sid)
            tabsChanged = true;
        if (std::none_of(incomingPeers.begin(), incomingPeers.end(), [&](const Inbound &other) {
                return other.sid == peer.sid;
            })) {
            chats.erase(peer.sid);
            inboundDrafts.erase(peer.sid);
        }
    }
    inboundPeers.swap(incomingPeers);
    inboundCount = int(inboundPeers.size());
    if (!inboundCount) {
        selectedInbound.clear();
        if (inboundWindow) {
            DestroyWindow(inboundWindow);
            inboundWindow = nullptr;
        }
        return;
    }
    if (!inboundWindow) {
        inboundWindow = CreateWindowExW(WS_EX_TOOLWINDOW | WS_EX_TOPMOST, L"NDInbound",
                          L"NanoDesk — 正在被遠端連線", WS_OVERLAPPEDWINDOW | WS_CLIPCHILDREN,
                          CW_USEDEFAULT, CW_USEDEFAULT, S(650), S(390), nullptr, nullptr, instance, nullptr);
        if (!inboundWindow)
            return;
        tabsChanged = true;
    }
    if (tabsChanged) {
        TabCtrl_DeleteAllItems(inboundTabs);
        int selected = 0;
        for (int i = 0; i < inboundCount; ++i) {
            const auto &peer = inboundPeers[i];
            tabText(inboundTabs, i, peer.device, true);
            if (peer.sid == oldSelection)
                selected = i;
        }
        selectedInbound = inboundPeers[selected].sid;
        TabCtrl_SetCurSel(inboundTabs, selected);
    }
    renderInbound(oldSelection != selectedInbound);
    if (!IsWindowVisible(inboundWindow))
        ShowWindow(inboundWindow, SW_SHOWNOACTIVATE);
}

extern "C" void ND_ChatText(const char *sid, const char *message, int local) {
    Remote *r = find(sid);
    bool inbound = std::any_of(inboundPeers.begin(), inboundPeers.end(), [sid](const Inbound &peer) {
        return peer.sid == sid;
    });
    if (!inbound && (!r || r->ended || !r->hwnd))
        return;
    auto &history = chats[sid];
    if (history.size() >= 64)
        history.pop_front();
    history.push_back((local > 0 ? L"我： " : local < 0 ? L"系統： " : L"對方： ") + wide(message));
    if (inbound && selectedInbound == sid)
        renderChat(inboundChat, sid);
    if (r) {
        if (r->chatWindow) {
            ChatControls chat = {GetDlgItem(r->chatWindow, 501), nullptr, nullptr};
            renderChat(chat, sid);
        } else {
            SetWindowTextW(r->chatButton, L"對話 •");
        }
    }
}

extern "C" int ND_IsActive(const char *sid) {
    Remote *r = find(sid);
    return r && canvasFocused(r);
}

static LRESULT CALLBACK mainProc(HWND h, UINT msg, WPARAM w, LPARAM l) {
    if (LRESULT themed = themeMessage(h, msg, w, l))
        return themed;
    switch (msg) {
    case WM_DISPLAYCHANGE:
        push(5);
        return 0;
    case WM_GETMINMAXINFO:
        ((MINMAXINFO *)l)->ptMinTrackSize = {S(680), S(520)};
        return 0;
    case WM_SIZE:
        if (tabs)
            layoutHost(h, selectedPage == 2 ? selectedRemote : nullptr);
        return 0;
    case WM_CREATE:
        mainWindow = h;
        buildMain(h);
        return 0;
    case WM_DRAWITEM:
        drawIcon((DRAWITEMSTRUCT *)l);
        return TRUE;
    case WM_NOTIFY:
        if (((NMHDR *)l)->idFrom == 100 && ((NMHDR *)l)->code == TCN_SELCHANGE) {
            int index = TabCtrl_GetCurSel(tabs);
            selectedRemote = index > 0 && index <= int(docked.size()) ? docked[index - 1] : nullptr;
            selectedPage = selectedRemote ? 2 : index > 0 ? 1 : 0;
            showPage();
            if (!dragTab && selectedRemote && selectedRemote->authenticated)
                SetFocus(selectedRemote->canvas);
        }
        return 0;
    case WM_COMMAND: {
        int id = LOWORD(w);
        if (id == 101) {
            settingsOpen = true;
            selectedPage = 1;
            selectedRemote = nullptr;
            rebuildTabs();
            showPage();
        } else if (id == 105 && (HIWORD(w) == EN_SETFOCUS || HIWORD(w) == EN_KILLFOCUS)) {
            InvalidateRect(h, nullptr, TRUE);
        } else if (id == 102)
            closeSettings();
        else if (id == 109 && selectedRemote)
            SendMessageW(selectedRemote->hwnd, WM_CLOSE, 0, 0);
        else if (id == 104)
            push(2, "", utf8(localID));
        else if (id == 106)
            push(1, "", utf8(text(targetEdit)));
        else if (id == 107)
            push(4);
        else if (id == 108 && HIWORD(w) == LBN_SELCHANGE) {
            selectedCategory = (int)SendMessageW(category, LB_GETCURSEL, 0, 0);
            settingsScroll = 0;
            showPage();
        } else if (id == 1100)
            save(l == 1);
        else if (id == 1210 && HIWORD(w) == BN_CLICKED)
            chooseCAFile();
        else if (id >= 1201 && id <= 1209 && !loading &&
                 (HIWORD(w) == EN_CHANGE || HIWORD(w) == BN_CLICKED || HIWORD(w) == CBN_SELCHANGE))
            dirty = true;
        return 0;
    }
    case WM_CLOSE:
        if (dirty) {
            closeSettings();
            if (dirty)
                return 0;
        }
        if (std::any_of(remotes.begin(), remotes.end(), [](const auto &entry) {
                return entry.second->hwnd && !entry.second->ended;
            }) || inboundCount > 0 || !approvals.empty()) {
            if (MessageBoxW(h, L"退出 NanoDesk 會中斷所有連線。確定退出？", L"退出 NanoDesk",
                            MB_YESNO | MB_DEFBUTTON2 | MB_ICONWARNING) != IDYES)
                return 0;
        }
        DestroyWindow(h);
        return 0;
    case WM_DESTROY:
        if (keyboardHook) {
            UnhookWindowsHookEx(keyboardHook);
            keyboardHook = nullptr;
        }
        running = false;
        push(3);
        PostQuitMessage(0);
        return 0;
    }
    return DefWindowProcW(h, msg, w, l);
}

static LRESULT CALLBACK keyboardProc(int code, WPARAM w, LPARAM l) {
    if (code == HC_ACTION) {
        auto *k = (KBDLLHOOKSTRUCT *)l;
        if (!(k->flags & LLKHF_INJECTED)) {
            HWND focus = GetFocus();
            Remote *target = nullptr;
            for (auto &item : remotes) {
                Remote *r = item.second.get();
                if (focus == r->canvas && canvasFocused(r)) {
                    target = r;
                    break;
                }
            }
            if (target) {
                bool alt = (k->flags & LLKHF_ALTDOWN) != 0,
                     ctrl = (GetAsyncKeyState(VK_CONTROL) & 0x8000) != 0;
                DWORD vk = k->vkCode;
                bool reserved = vk == VK_LWIN || vk == VK_RWIN ||
                                (alt && (vk == VK_TAB || vk == VK_ESCAPE || vk == VK_F4)) ||
                                (ctrl && vk == VK_ESCAPE);
                if (reserved) {
                    push(23, target->sid, "", int(vk), (w == WM_KEYDOWN || w == WM_SYSKEYDOWN) ? 1 : 0,
                         (k->flags & LLKHF_EXTENDED) ? 1 : 0, int(k->scanCode));
                    return 1;
                }
            }
        }
    }
    return CallNextHookEx(keyboardHook, code, w, l);
}

extern "C" int ND_Init() {
    instance = GetModuleHandleW(nullptr);
    HMODULE user = GetModuleHandleW(L"user32.dll");
    auto dpi = (BOOL(WINAPI *)(HANDLE))GetProcAddress(user, "SetProcessDpiAwarenessContext");
    if (dpi)
        dpi((HANDLE)-2);
    CoInitializeEx(nullptr, COINIT_APARTMENTTHREADED);
    if (FAILED(MFStartup(MF_VERSION)))
        return 0;
    INITCOMMONCONTROLSEX cc = {sizeof(cc), ICC_TAB_CLASSES | ICC_STANDARD_CLASSES};
    InitCommonControlsEx(&cc);
    HDC screen = GetDC(nullptr);
    uiScale = GetDeviceCaps(screen, LOGPIXELSX) / 96.;
    ReleaseDC(nullptr, screen);
    auto makeFont = [](int size, int weight, const wchar_t *face) {
        return CreateFontW(-S(size), 0, 0, 0, weight, FALSE, FALSE, FALSE, DEFAULT_CHARSET, 0, 0,
                           CLEARTYPE_QUALITY, 0, face);
    };
    font = makeFont(14, FW_NORMAL, L"Microsoft JhengHei UI");
    titleFont = makeFont(24, FW_SEMIBOLD, L"Microsoft JhengHei UI");
    smallFont = makeFont(12, FW_NORMAL, L"Microsoft JhengHei UI");
    idFont = makeFont(28, FW_SEMIBOLD, L"Consolas");
    paperBrush = CreateSolidBrush(paper);
    shellBrush = CreateSolidBrush(shell);
    HIGHCONTRASTW contrast = {sizeof(contrast)};
    SystemParametersInfoW(SPI_GETHIGHCONTRAST, sizeof(contrast), &contrast, 0);
    highContrast = (contrast.dwFlags & HCF_HIGHCONTRASTON) != 0;
    struct Registration {
        const wchar_t *name;
        WNDPROC proc;
    };
    Registration classes[] = {{L"NDMain", mainProc},        {L"NDRemote", remoteProc},
                              {L"NDCanvas", canvasProc},    {L"NDViewport", viewportProc},
                              {L"NDMonitors", monitorProc}, {L"NDApproval", approvalProc},
                              {L"NDPanel", panelProc}, {L"NDDetached", detachedProc},
                              {L"NDInbound", inboundProc}, {L"NDChat", chatProc},
                              {L"NDSettings", settingsProc}};
    for (auto c : classes) {
        WNDCLASSEXW wc = {};
        wc.cbSize = sizeof(wc);
        wc.lpfnWndProc = c.proc;
        wc.hInstance = instance;
        wc.hCursor = LoadCursorW(nullptr, IDC_ARROW);
        wc.hbrBackground = (HBRUSH)(COLOR_BTNFACE + 1);
        wc.lpszClassName = c.name;
        wc.hIcon = LoadIconW(instance, MAKEINTRESOURCEW(1));
        RegisterClassExW(&wc);
    }
    mainWindow = CreateWindowExW(0, L"NDMain", L"NanoDesk APP",
                                 WS_OVERLAPPEDWINDOW | WS_CLIPCHILDREN, CW_USEDEFAULT,
                                 CW_USEDEFAULT, S(900), S(630), nullptr, nullptr, instance, nullptr);
    if (!mainWindow)
        return 0;
    SetWindowSubclass(tabs, tabProc, 1, 0);
    keyboardHook = SetWindowsHookExW(WH_KEYBOARD_LL, keyboardProc, instance, 0);
    ShowWindow(mainWindow, SW_SHOW);
    return 1;
}
extern "C" int ND_Poll(NDEvent *out) {
    MSG msg;
    int count = 0;
    while (count++ < 64 && PeekMessageW(&msg, nullptr, 0, 0, PM_REMOVE)) {
        if (msg.message == WM_QUIT) {
            running = false;
            break;
        }
        HWND root = GetAncestor(msg.hwnd, GA_ROOT);
        wchar_t cls[64] = {};
        GetClassNameW(msg.hwnd, cls, 64);
        bool canvas = wcscmp(cls, L"NDCanvas") == 0;
        if (dragTab && msg.message == WM_KEYDOWN && msg.wParam == VK_ESCAPE) {
            ReleaseCapture();
            continue;
        }
        if (msg.message == WM_KEYDOWN && msg.wParam == VK_RETURN && msg.hwnd == targetEdit) {
            SendMessageW(mainWindow, WM_COMMAND, 106, 0);
            continue;
        }
        if (msg.message == WM_KEYDOWN && msg.wParam == VK_RETURN && GetDlgCtrlID(msg.hwnd) == 502) {
            SendMessageW(root, WM_COMMAND, 503, 0);
            continue;
        }
        if (!canvas && IsDialogMessageW(root, &msg)) {
            syncInput();
            continue;
        }
        TranslateMessage(&msg);
        DispatchMessageW(&msg);
        syncInput();
    }
    if (!events.empty()) {
        *out = events.front();
        events.pop_front();
        return 1;
    }
    return running ? 0 : -1;
}
extern "C" void ND_MainText(int which, const char *s) {
    if (which == 4) {
        dirty = false;
        closeSettings();
        return;
    }
    if (which == 0) {
        localID = wide(s);
        std::wstring display = localID;
        if (display.size() == 10) {
            display.insert(7, 1, L' ');
            display.insert(4, 1, L' ');
            display.insert(1, 1, L' ');
        }
        SendMessageW(idText, WM_SETFONT, (WPARAM)(display.empty() ? titleFont : idFont), TRUE);
        SetWindowTextW(idText, display.empty() ? L"尚未取得 ID" : display.c_str());
        EnableWindow(copyID, !localID.empty());
    } else {
        SetWindowTextW(which == 1 ? statusText : inboundText, wide(s).c_str());
    }
}
extern "C" void ND_Settings(const char *server, const char *ca, int unattended, const char *v, const char *s,
                            const char *a, int64_t limit, int clip, int relay, int tlsOnly) {
    loading = true;
    SendMessageW(modeCombo, CB_SETCURSEL, tlsOnly ? 2 : relay ? 1 : 0, 0);
    SetWindowTextW(serverEdit, wide(server).c_str());
    SetWindowTextW(caEdit, wide(ca).c_str());
    SetWindowTextW(viewEdit, wide(v).c_str());
    SetWindowTextW(standardEdit, wide(s).c_str());
    SetWindowTextW(adminEdit, wide(a).c_str());
    SetWindowTextW(limitEdit, std::to_wstring(limit).c_str());
    SendMessageW(unattendedCheck, BM_SETCHECK, unattended ? BST_CHECKED : BST_UNCHECKED, 0);
    SendMessageW(clipboardCheck, BM_SETCHECK, clip ? BST_CHECKED : BST_UNCHECKED, 0);
    loading = false;
    dirty = false;
}
extern "C" int ND_BindSession(const char *pending, const char *sid) {
    if (find(sid))
        return 0;
    Remote *r = find(pending);
    if (!r || !r->hwnd || r->ended)
        return 0;
    auto entry = remotes.extract(pending);
    entry.key() = sid;
    r->sid = sid;
    remotes.insert(std::move(entry));
    return 1;
}

extern "C" void ND_Open(const char *sid, const char *id) {
    if (find(sid))
        return;
    auto ptr = std::make_unique<Remote>();
    Remote *r = ptr.get();
    r->sid = sid;
    r->targetID = wide(id);
    remotes[sid] = std::move(ptr);
    r->hwnd = CreateWindowExW(WS_EX_CONTROLPARENT, L"NDRemote", L"", WS_CHILD | WS_CLIPCHILDREN,
                             0, 0, 1, 1, mainWindow, nullptr, instance, nullptr);
    if (!r->hwnd) {
        remotes.erase(sid);
        push(28, sid);
        return;
    }
    SetWindowLongPtrW(r->hwnd, GWLP_USERDATA, (LONG_PTR)r);
    r->tooltip =
        CreateWindowExW(WS_EX_TOPMOST, TOOLTIPS_CLASSW, nullptr, WS_POPUP | TTS_ALWAYSTIP, CW_USEDEFAULT,
                        CW_USEDEFAULT, CW_USEDEFAULT, CW_USEDEFAULT, r->hwnd, nullptr, instance, nullptr);
    SendMessageW(r->tooltip, TTM_SETDELAYTIME, TTDT_INITIAL, 500);
    SendMessageW(r->tooltip, TTM_SETMAXTIPWIDTH, 0, 380);
    r->monitors = control(r->hwnd, L"NDMonitors", L"螢幕選擇", WS_HSCROLL, 200, 8, 8, 360, 38);
    SetWindowLongPtrW(r->monitors, GWLP_USERDATA, (LONG_PTR)r);
    const wchar_t *glyphs[] = {L"⛶", L"↔", L"⌨", L"▤", L"⇧", L"✎", L"♟", L"ⓘ", L"⏻"};
    const wchar_t *names[] = {L"縮放與全螢幕", L"主控權", L"快捷鍵",   L"剪貼簿",  L"檔案傳輸",
                              L"協同指示筆",   L"成員",   L"連線資訊", L"中斷連線"};
    for (int i = 0; i < 9; i++) {
        HWND b = icon(r->hwnd, r->tooltip, 210 + i, glyphs[i], names[i], 400 + i * 40, 8);
        r->tools.push_back(b);
        EnableWindow(b, FALSE);
    }
    EnableWindow(r->tools.back(), TRUE);
    r->status = label(r->hwnd, L"正在建立加密連線…", 12, 48, 900, 24);
    r->chatButton = control(r->hwnd, L"BUTTON", L"對話", WS_TABSTOP, 219, 0, 0, 84, 28);
    EnableWindow(r->chatButton, FALSE);
    r->viewport = control(r->hwnd, L"NDViewport", L"遠端桌面", WS_HSCROLL | WS_VSCROLL | WS_CLIPCHILDREN, 220,
                          0, 76, 1000, 600);
    SetWindowLongPtrW(r->viewport, GWLP_USERDATA, (LONG_PTR)r);
    r->canvas = control(r->viewport, L"NDCanvas", L"遠端畫面", WS_TABSTOP, 221, 0, 0, 1000, 600);
    SetWindowLongPtrW(r->canvas, GWLP_USERDATA, (LONG_PTR)r);
    r->decoder = std::make_unique<Decoder>(r->canvas);
    ShowWindow(r->viewport, SW_HIDE);
    r->authLabel = label(r->hwnd, L"請求被控端授權，或輸入無人值守密碼", 70, 140, 580, 30);
    r->request = control(r->hwnd, L"BUTTON", L"請求授權", WS_TABSTOP, 201, 70, 190, 160, 36);
    r->password = control(r->hwnd, L"EDIT", L"", WS_BORDER | ES_PASSWORD | WS_TABSTOP, 203, 70, 258, 300, 34);
    SendMessageW(r->password, EM_SETLIMITTEXT, 64, 0);
    r->login = control(r->hwnd, L"BUTTON", L"密碼登入", WS_TABSTOP, 202, 390, 258, 120, 34);
    EnableWindow(r->request, FALSE);
    EnableWindow(r->login, FALSE);
    layout(r);
    SetTimer(r->hwnd, 1, 1000, nullptr);
    docked.push_back(r);
    selectedPage = 2;
    selectedRemote = r;
    rebuildTabs();
    showPage();
    SetFocus(tabs);
}
extern "C" void ND_RemoteText(const char *sid, const char *s) {
    Remote *r = find(sid);
    if (!r || !r->hwnd)
        return;
    std::wstring message = wide(s);
    if (message == L"直連" || message == L"中繼") {
        r->route = message;
        refreshTab(r);
        r->info = L"目前使用" + message + L"連線";
        updateTip(r, r->tools[7], r->info);
        if (IsWindow(r->infoPanel))
            SetWindowTextW(GetDlgItem(r->infoPanel, 400), connectionDetails(r).c_str());
    }
    if (message == L"new" || message == L"connecting")
        icons[r->tools[7]].state = 1;
    else if (message == L"failed" || message == L"disconnected" ||
             message.find(L"失敗") != std::wstring::npos || message.find(L"錯誤") != std::wstring::npos ||
             message.find(L"拒絕") != std::wstring::npos || message.find(L"逾時") != std::wstring::npos)
        icons[r->tools[7]].state = 2;
    else
        icons[r->tools[7]].state = 0;
    InvalidateRect(r->tools[7], nullptr, TRUE);
    r->noticeUntil = GetTickCount64() + 3000;
    SetWindowTextW(r->status, message.c_str());
    if (message == L"通道已就緒") {
        r->phase = L"等待授權";
        refreshTab(r);
        EnableWindow(r->request, TRUE);
        EnableWindow(r->login, TRUE);
    }
}
extern "C" void ND_Authenticated(const char *sid, int role) {
    Remote *r = find(sid);
    if (!r || !r->hwnd)
        return;
    r->role = role;
    r->authenticated = true;
    r->phase = L"已連線";
    EnableWindow(r->chatButton, TRUE);
    refreshTab(r);
    icons[r->tools[7]].state = 0;
    InvalidateRect(r->tools[7], nullptr, TRUE);
    r->lastFrame = GetTickCount64();
    for (HWND h : {r->authLabel, r->request, r->password, r->login})
        ShowWindow(h, SW_HIDE);
    ShowWindow(r->viewport, SW_SHOW);
    for (size_t i = 0; i < r->tools.size(); i++)
        EnableWindow(r->tools[i], i == 0 || i == 5 || i >= 6 || role >= 2);
    if (IsWindowVisible(r->hwnd) && GetForegroundWindow() == hostOf(r))
        SetFocus(r->canvas);
    syncInput();
}
extern "C" void ND_ClearMonitors(const char *sid) {
    Remote *r = find(sid);
    if (!r || !r->hwnd)
        return;
    for (HWND h : r->screens) {
        TOOLINFOW ti = {};
        ti.cbSize = sizeof(ti);
        ti.hwnd = r->monitors;
        ti.uFlags = TTF_IDISHWND;
        ti.uId = (UINT_PTR)h;
        SendMessageW(r->tooltip, TTM_DELTOOLW, 0, (LPARAM)&ti);
        icons.erase(h);
        DestroyWindow(h);
    }
    r->screens.clear();
}
extern "C" void ND_Monitor(const char *sid, int index, int number, int w, int h, int primary, int selected) {
    Remote *r = find(sid);
    if (!r || !r->hwnd)
        return;
    std::wstring name = L"螢幕 " + std::to_wstring(number) + L" — " + std::to_wstring(w) + L" × " +
                        std::to_wstring(h) + (primary ? L"（主要螢幕）" : L"");
    HWND b = icon(r->monitors, r->tooltip, 1000 + index, std::to_wstring(number), name, 0, 0);
    icons[b].monitor = index;
    icons[b].selected = selected;
    EnableWindow(b, r->role >= 2);
    r->screens.push_back(b);
    if (selected) {
        r->sourceWidth = w;
        r->sourceHeight = h;
        r->decoder->visibleWidth = w;
        r->decoder->visibleHeight = h;
    }
    layout(r);
}
extern "C" void ND_Control(const char *sid, int controller, int local) {
    Remote *r = find(sid);
    if (!r || !r->hwnd)
        return;
    icons[r->tools[1]].selected = controller;
    updateTip(r, r->tools[1],
              local        ? L"主控權：本機操作優先"
              : controller ? L"主控權：目前由你控制"
                           : L"主控權：點擊申請或接管");
    InvalidateRect(r->tools[1], nullptr, TRUE);
    if (local) {
        r->noticeUntil = GetTickCount64() + 2000;
        SetWindowTextW(r->status, L"被控端本機操作中，遠端輸入暫停");
    }
}
extern "C" void ND_Approval(const char *sid, const char *request, const char *identity) {
    for (auto &a : approvals)
        if (a.second.sid == sid)
            return;
    HWND h = CreateWindowExW(WS_EX_TOPMOST | WS_EX_TOOLWINDOW, L"NDApproval", L"NanoDesk — 連線授權",
                             WS_OVERLAPPED | WS_CAPTION | WS_SYSMENU, CW_USEDEFAULT, CW_USEDEFAULT, S(590),
                             S(230), nullptr, nullptr, instance, nullptr);
    if (!h)
        return;
    approvals[h] = {sid, request, h, GetTickCount64()};
    label(h, (L"裝置 " + wide(identity) + L" 請求連線\n請選擇授權角色（60 秒內有效）").c_str(), 20, 20, 540,
          70);
    const wchar_t *names[] = {L"僅觀看", L"一般控制", L"完全控制", L"拒絕"};
    for (int i = 0; i < 4; i++)
        control(h, L"BUTTON", names[i], WS_TABSTOP, 301 + i, 20 + i * 135, 120, 125, 34);
    SetTimer(h, 1, 1000, nullptr);
    ShowWindow(h, SW_SHOWNOACTIVATE);
}
extern "C" void ND_CloseApproval(const char *sid) {
    std::vector<HWND> close;
    for (auto &a : approvals)
        if (a.second.sid == sid)
            close.push_back(a.first);
    for (HWND h : close)
        DestroyWindow(h);
}
extern "C" void ND_Close(const char *sid) {
    ND_CloseApproval(sid);
    Remote *r = find(sid);
    if (!r)
        return;
    if (r->hwnd) {
        bool wasAuthenticated = r->authenticated;
        r->ended = true;
        r->authenticated = false;
        r->phase = L"已中斷";
        EnableWindow(r->chatButton, FALSE);
        if (IsWindow(r->chatWindow))
            DestroyWindow(r->chatWindow);
        refreshTab(r);
        syncInput();
        for (HWND control : {r->request, r->password, r->login})
            ShowWindow(control, SW_HIDE);
        r->decoder.reset();
        if (wasAuthenticated || text(r->status) == L"正在建立加密連線…")
            SetWindowTextW(r->status, L"連線已結束");
        for (HWND h : r->tools)
            EnableWindow(h, FALSE);
        for (HWND h : r->screens)
            EnableWindow(h, FALSE);
        ShowWindow(r->viewport, SW_HIDE);
        ShowWindow(r->authLabel, SW_SHOW);
        SetWindowTextW(r->authLabel, L"連線已結束，請關閉視窗後重新連線。");
    }
}
extern "C" void ND_Error(const char *s) {
    MessageBoxW(mainWindow, wide(s).c_str(), L"NanoDesk", MB_OK | MB_ICONINFORMATION);
}
extern "C" int ND_Frame(const char *sid, const unsigned char *data, int length) {
    Remote *r = find(sid);
    if (!r || !r->hwnd || r->ended || !r->authenticated)
        return 0;
    unsigned width = r->decoder->width, height = r->decoder->height;
    int result = r->decoder->feed(data, length);
    if (result > 0) {
        r->lastFrame = GetTickCount64();
        if (r->lastFrame > r->noticeUntil && r->noticeUntil != 0) {
            r->noticeUntil = 0;
            SetWindowTextW(r->status, L"已連線");
        }
        if (width != r->decoder->width || height != r->decoder->height)
            layout(r);
    }
    return result;
}

extern "C" void ND_Panel(const char *sid, const char *s, int members) {
    Remote *r = find(sid);
    if (!r || !r->hwnd)
        return;
    if (members)
        r->members = wide(s);
    else
        r->info = wide(s);
    HWND panel = members ? r->membersPanel : r->infoPanel;
    if (IsWindow(panel))
        SetWindowTextW(GetDlgItem(panel, 400), members ? r->members.c_str() : connectionDetails(r).c_str());
}
extern "C" void ND_Laser(const char *sid, const char *id, const char *name, const char *color, double x,
                         double y) {
    Remote *r = find(sid);
    if (!r || !r->hwnd)
        return;
    unsigned c = 0x44aaff;
    if (color && color[0] == '#')
        sscanf(color + 1, "%x", &c);
    pointers[sid][id] = {x, y, wide(name), RGB((c >> 16) & 255, (c >> 8) & 255, c & 255), GetTickCount64()};
    InvalidateRect(r->canvas, nullptr, FALSE);
}

extern "C" void ND_Clipboard(const char *sid, int enabled) {
    Remote *r = find(sid);
    if (!r || !r->hwnd)
        return;
    r->clip = enabled;
    updateTip(r, r->tools[3], enabled ? L"剪貼簿同步：開啟" : L"剪貼簿同步：關閉");
    icons[r->tools[3]].selected = enabled;
    InvalidateRect(r->tools[3], nullptr, TRUE);
}

extern "C" void ND_Forget(const char *sid) {
    Remote *r = find(sid);
    if (!r || r->hwnd)
        return;
    pointers.erase(sid);
    chats.erase(sid);
    remotes.erase(sid);
}
