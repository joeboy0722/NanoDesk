#include <windows.h>
#include <ole2.h>
#include <shlobj.h>
#include <algorithm>
#include <cstring>
#include <memory>
#include <new>
#include <string>
#include <vector>

extern "C" int ND_ReadClipboardFile(uintptr_t, int, long long, void *, int);
extern "C" void ND_ReleaseClipboardFiles(uintptr_t);

struct VirtualItem {
    std::wstring name;
    long long size;
    bool directory;
};

struct VirtualFiles {
    uintptr_t token = 0;
    std::vector<VirtualItem> items;
    std::vector<std::pair<CLIPFORMAT, std::vector<char>>> formats;
    ~VirtualFiles() { if (token) ND_ReleaseClipboardFiles(token); }
};

class FileStream final : public IStream {
    ULONG refs = 1;
    std::shared_ptr<VirtualFiles> files;
    int index;
    long long position = 0;
public:
    FileStream(std::shared_ptr<VirtualFiles> files, int index) : files(std::move(files)), index(index) {}
    HRESULT STDMETHODCALLTYPE QueryInterface(REFIID iid, void **object) override {
        if (!object) return E_POINTER;
        *object = nullptr;
        if (iid != IID_IUnknown && iid != IID_ISequentialStream && iid != IID_IStream) return E_NOINTERFACE;
        *object = static_cast<IStream *>(this);
        AddRef();
        return S_OK;
    }
    ULONG STDMETHODCALLTYPE AddRef() override { return ++refs; }
    ULONG STDMETHODCALLTYPE Release() override {
        const auto count = --refs;
        if (!count) delete this;
        return count;
    }
    HRESULT STDMETHODCALLTYPE Read(void *buffer, ULONG count, ULONG *read) override {
        if (read) *read = 0;
        if (!buffer && count) return STG_E_INVALIDPOINTER;
        auto remaining = std::max(0LL, files->items[index].size - position);
        auto requested = static_cast<ULONG>(std::min<long long>(count, remaining));
        ULONG done = 0;
        while (done < requested) {
            int chunk = static_cast<int>(std::min<ULONG>(requested - done, 8192));
            int result = ND_ReadClipboardFile(files->token, index, position, static_cast<char *>(buffer) + done, chunk);
            if (result != chunk) return STG_E_READFAULT;
            done += result;
            position += result;
            if (read) *read = done;
        }
        return done == count ? S_OK : S_FALSE;
    }
    HRESULT STDMETHODCALLTYPE Seek(LARGE_INTEGER offset, DWORD origin, ULARGE_INTEGER *result) override {
        long long base = 0;
        if (origin == STREAM_SEEK_CUR) base = position;
        else if (origin == STREAM_SEEK_END) base = files->items[index].size;
        else if (origin != STREAM_SEEK_SET) return STG_E_INVALIDFUNCTION;
        if (offset.QuadPart < -base || offset.QuadPart > LLONG_MAX - base) return STG_E_INVALIDFUNCTION;
        position = base + offset.QuadPart;
        if (result) result->QuadPart = position;
        return S_OK;
    }
    HRESULT STDMETHODCALLTYPE Stat(STATSTG *stat, DWORD) override {
        if (!stat) return E_POINTER;
        *stat = {};
        stat->type = STGTY_STREAM;
        stat->cbSize.QuadPart = files->items[index].size;
        stat->grfMode = STGM_READ;
        return S_OK;
    }
    HRESULT STDMETHODCALLTYPE Clone(IStream **stream) override {
        if (!stream) return E_POINTER;
        auto copy = new (std::nothrow) FileStream(files, index);
        if (!copy) return E_OUTOFMEMORY;
        copy->position = position;
        *stream = copy;
        return S_OK;
    }
    HRESULT STDMETHODCALLTYPE CopyTo(IStream *target, ULARGE_INTEGER size, ULARGE_INTEGER *read, ULARGE_INTEGER *written) override {
        if (!target) return E_POINTER;
        if (read) read->QuadPart = 0;
        if (written) written->QuadPart = 0;
        char buffer[8192];
        while (size.QuadPart) {
            ULONG n = 0, out = 0;
            auto hr = Read(buffer, static_cast<ULONG>(std::min<ULONGLONG>(size.QuadPart, sizeof(buffer))), &n);
            if (read) read->QuadPart += n;
            if (FAILED(hr)) return hr;
            if (!n) return S_OK;
            hr = target->Write(buffer, n, &out);
            if (written) written->QuadPart += out;
            if (FAILED(hr)) return hr;
            if (out != n) return STG_E_MEDIUMFULL;
            size.QuadPart -= n;
        }
        return S_OK;
    }
    HRESULT STDMETHODCALLTYPE Write(const void *, ULONG, ULONG *) override { return STG_E_ACCESSDENIED; }
    HRESULT STDMETHODCALLTYPE SetSize(ULARGE_INTEGER) override { return STG_E_ACCESSDENIED; }
    HRESULT STDMETHODCALLTYPE Commit(DWORD) override { return S_OK; }
    HRESULT STDMETHODCALLTYPE Revert() override { return STG_E_INVALIDFUNCTION; }
    HRESULT STDMETHODCALLTYPE LockRegion(ULARGE_INTEGER, ULARGE_INTEGER, DWORD) override { return STG_E_INVALIDFUNCTION; }
    HRESULT STDMETHODCALLTYPE UnlockRegion(ULARGE_INTEGER, ULARGE_INTEGER, DWORD) override { return STG_E_INVALIDFUNCTION; }
};

class FileClipboard final : public IDataObject {
    ULONG refs = 1;
    std::shared_ptr<VirtualFiles> files;
    CLIPFORMAT descriptors = static_cast<CLIPFORMAT>(RegisterClipboardFormatW(CFSTR_FILEDESCRIPTORW));
    CLIPFORMAT contents = static_cast<CLIPFORMAT>(RegisterClipboardFormatW(CFSTR_FILECONTENTS));
    CLIPFORMAT effect = static_cast<CLIPFORMAT>(RegisterClipboardFormatW(CFSTR_PREFERREDDROPEFFECT));
public:
    explicit FileClipboard(std::shared_ptr<VirtualFiles> files) : files(std::move(files)) {}
    HRESULT STDMETHODCALLTYPE QueryInterface(REFIID iid, void **object) override {
        if (!object) return E_POINTER;
        *object = nullptr;
        if (iid != IID_IUnknown && iid != IID_IDataObject) return E_NOINTERFACE;
        *object = static_cast<IDataObject *>(this);
        AddRef();
        return S_OK;
    }
    ULONG STDMETHODCALLTYPE AddRef() override { return ++refs; }
    ULONG STDMETHODCALLTYPE Release() override {
        const auto count = --refs;
        if (!count) delete this;
        return count;
    }
    HRESULT STDMETHODCALLTYPE QueryGetData(FORMATETC *format) override {
        if (!format) return E_POINTER;
        if (format->dwAspect != DVASPECT_CONTENT) return DV_E_DVASPECT;
        if (format->cfFormat == contents) {
            if (!(format->tymed & TYMED_ISTREAM)) return DV_E_TYMED;
            if (format->lindex == -1) return S_OK;
            if (format->lindex < 0 || static_cast<size_t>(format->lindex) >= files->items.size() ||
                files->items[format->lindex].directory) return DV_E_LINDEX;
            return S_OK;
        }
        if (format->cfFormat != descriptors && format->cfFormat != effect &&
            std::none_of(files->formats.begin(), files->formats.end(), [&](const auto &entry) {
                return entry.first == format->cfFormat;
            })) return DV_E_FORMATETC;
        return format->tymed & TYMED_HGLOBAL ? S_OK : DV_E_TYMED;
    }
    HRESULT STDMETHODCALLTYPE GetData(FORMATETC *format, STGMEDIUM *medium) override {
        if (!medium) return E_POINTER;
        *medium = {};
        auto hr = QueryGetData(format);
        if (FAILED(hr)) return hr;
        if (format->cfFormat == contents) {
            if (format->lindex < 0) return DV_E_LINDEX;
            auto stream = new (std::nothrow) FileStream(files, format->lindex);
            if (!stream) return E_OUTOFMEMORY;
            medium->tymed = TYMED_ISTREAM;
            medium->pstm = stream;
            return S_OK;
        }
        const auto raw = std::find_if(files->formats.begin(), files->formats.end(), [&](const auto &entry) {
            return entry.first == format->cfFormat;
        });
        const bool isEffect = format->cfFormat == effect;
        auto size = isEffect ? sizeof(DWORD) : offsetof(FILEGROUPDESCRIPTORW, fgd) + files->items.size() * sizeof(FILEDESCRIPTORW);
        if (raw != files->formats.end()) size = raw->second.size();
        auto memory = GlobalAlloc(GMEM_MOVEABLE | GMEM_ZEROINIT, size);
        if (!memory) return E_OUTOFMEMORY;
        auto ptr = GlobalLock(memory);
        if (!ptr) {
            GlobalFree(memory);
            return E_OUTOFMEMORY;
        }
        if (raw != files->formats.end()) {
            std::memcpy(ptr, raw->second.data(), size);
        } else if (isEffect) {
            *static_cast<DWORD *>(ptr) = DROPEFFECT_COPY;
        } else {
            auto group = static_cast<FILEGROUPDESCRIPTORW *>(ptr);
            group->cItems = static_cast<UINT>(files->items.size());
            auto entries = reinterpret_cast<FILEDESCRIPTORW *>(static_cast<char *>(ptr) + offsetof(FILEGROUPDESCRIPTORW, fgd));
            for (size_t i = 0; i < files->items.size(); ++i) {
                const auto &item = files->items[i];
                auto &entry = entries[i];
                entry.dwFlags = FD_ATTRIBUTES | FD_UNICODE | FD_PROGRESSUI;
                entry.dwFileAttributes = item.directory ? FILE_ATTRIBUTE_DIRECTORY : FILE_ATTRIBUTE_NORMAL;
                if (!item.directory) {
                    entry.dwFlags |= FD_FILESIZE;
                    entry.nFileSizeHigh = static_cast<DWORD>(item.size >> 32);
                    entry.nFileSizeLow = static_cast<DWORD>(item.size);
                }
                std::copy(item.name.c_str(), item.name.c_str() + item.name.size() + 1, entry.cFileName);
            }
        }
        GlobalUnlock(memory);
        medium->tymed = TYMED_HGLOBAL;
        medium->hGlobal = memory;
        return S_OK;
    }
    HRESULT STDMETHODCALLTYPE EnumFormatEtc(DWORD direction, IEnumFORMATETC **formats) override {
        if (direction != DATADIR_GET) return E_NOTIMPL;
        try {
            std::vector<FORMATETC> list = {
                {descriptors, nullptr, DVASPECT_CONTENT, -1, TYMED_HGLOBAL},
                {contents, nullptr, DVASPECT_CONTENT, -1, TYMED_ISTREAM},
                {effect, nullptr, DVASPECT_CONTENT, -1, TYMED_HGLOBAL}
            };
            for (const auto &raw : files->formats)
                list.push_back({raw.first, nullptr, DVASPECT_CONTENT, -1, TYMED_HGLOBAL});
            return SHCreateStdEnumFmtEtc(static_cast<UINT>(list.size()), list.data(), formats);
        } catch (...) {
            return E_OUTOFMEMORY;
        }
    }
    HRESULT STDMETHODCALLTYPE GetDataHere(FORMATETC *, STGMEDIUM *) override { return E_NOTIMPL; }
    HRESULT STDMETHODCALLTYPE GetCanonicalFormatEtc(FORMATETC *, FORMATETC *out) override {
        if (out) out->ptd = nullptr;
        return DATA_S_SAMEFORMATETC;
    }
    HRESULT STDMETHODCALLTYPE SetData(FORMATETC *, STGMEDIUM *, BOOL) override { return E_NOTIMPL; }
    HRESULT STDMETHODCALLTYPE DAdvise(FORMATETC *, DWORD, IAdviseSink *, DWORD *) override { return OLE_E_ADVISENOTSUPPORTED; }
    HRESULT STDMETHODCALLTYPE DUnadvise(DWORD) override { return OLE_E_ADVISENOTSUPPORTED; }
    HRESULT STDMETHODCALLTYPE EnumDAdvise(IEnumSTATDATA **) override { return OLE_E_ADVISENOTSUPPORTED; }
};

extern "C" void *ND_NewClipboardFiles(uintptr_t token) {
    try {
        auto files = std::make_unique<VirtualFiles>();
        files->token = token;
        return files.release();
    } catch (...) {
        ND_ReleaseClipboardFiles(token);
        return nullptr;
    }
}

extern "C" int ND_AddClipboardFile(void *offer, const wchar_t *name, long long size, int directory) {
    auto files = static_cast<VirtualFiles *>(offer);
    if (!name || !*name || wcslen(name) >= MAX_PATH || size < 0) return 0;
    try {
        files->items.push_back({name, size, directory != 0});
        return 1;
    } catch (...) {
        return 0;
    }
}

extern "C" int ND_AddClipboardFormat(void *offer, const wchar_t *name, const char *data, int size) {
    auto files = static_cast<VirtualFiles *>(offer);
    CLIPFORMAT id = 0;
    if (wcscmp(name, L"UnicodeText") == 0) id = CF_UNICODETEXT;
    else if (wcscmp(name, L"DIB") == 0) id = CF_DIB;
    else id = static_cast<CLIPFORMAT>(RegisterClipboardFormatW(name));
    if (!id || size <= 0) return 0;
    try {
        files->formats.emplace_back(id, std::vector<char>(data, data + size));
        return 1;
    } catch (...) {
        return 0;
    }
}

extern "C" void ND_DeleteClipboardFiles(void *offer) {
    delete static_cast<VirtualFiles *>(offer);
}

extern "C" long ND_PublishClipboardFiles(void *offer, unsigned long expected) {
    std::unique_ptr<VirtualFiles> owner(static_cast<VirtualFiles *>(offer));
    if (GetClipboardSequenceNumber() != expected) return HRESULT_FROM_WIN32(ERROR_RETRY);
    try {
        auto object = new FileClipboard(std::shared_ptr<VirtualFiles>(std::move(owner)));
        auto result = OleSetClipboard(object);
        object->Release();
        return result;
    } catch (...) {
        return E_OUTOFMEMORY;
    }
}

extern "C" int ND_TestClipboardObject(uintptr_t token) {
    auto files = std::make_shared<VirtualFiles>();
    files->token = token;
    files->items.push_back({L"folder", 0, true});
    files->items.push_back({L"folder\\file.txt", 5, false});
    FileClipboard object(files);
    IEnumFORMATETC *formats = nullptr;
    if (FAILED(object.EnumFormatEtc(DATADIR_GET, &formats))) return 1;
    FORMATETC descriptor{}, content{};
    ULONG fetched = 0;
    auto hr = formats->Next(1, &descriptor, &fetched);
    if (hr == S_OK) hr = formats->Next(1, &content, &fetched);
    formats->Release();
    if (hr != S_OK || object.QueryGetData(&content) != S_OK) return 2;
    STGMEDIUM data{};
    if (FAILED(object.GetData(&descriptor, &data))) return 3;
    auto group = static_cast<FILEGROUPDESCRIPTORW *>(GlobalLock(data.hGlobal));
    bool valid = group && group->cItems == 2 &&
        group->fgd[0].dwFileAttributes == FILE_ATTRIBUTE_DIRECTORY &&
        group->fgd[1].nFileSizeLow == 5;
    if (group) GlobalUnlock(data.hGlobal);
    ReleaseStgMedium(&data);
    if (!valid) return 4;
    content.lindex = 1;
    if (FAILED(object.GetData(&content, &data))) return 5;
    STATSTG stat{};
    hr = data.pstm->Stat(&stat, STATFLAG_NONAME);
    if (FAILED(hr) || stat.cbSize.QuadPart != 5) {
        ReleaseStgMedium(&data);
        return 6;
    }
    char buffer[8]{};
    ULONG read = 0;
    hr = data.pstm->Read(buffer, sizeof(buffer), &read);
    ReleaseStgMedium(&data);
    return hr == S_FALSE && read == 5 && std::memcmp(buffer, "hello", 5) == 0 ? 0 : 7;
}
