#pragma once
#include <stdint.h>
#ifdef __cplusplus
extern "C" {
#endif
typedef struct {
    int kind;
    int a;
    int b;
    double x;
    double y;
    char session[64];
    char text[8192];
} NDEvent;
int ND_Init(void);
int ND_Poll(NDEvent *);
void ND_MainText(int which, const char *text);
void ND_Settings(const char *server, const char *ca, int unattended, const char *v, const char *s,
                 const char *a, int64_t limit, int clipboard, int relay, int tlsOnly);
void ND_InboundBegin(void);
void ND_InboundAdd(const char *session, const char *device, const char *role, int controller);
void ND_InboundEnd(void);
void ND_ChatText(const char *session, const char *message, int local);
int ND_IsActive(const char *session);
int ND_BindSession(const char *pending, const char *session);
void ND_Open(const char *session, const char *id);
void ND_RemoteText(const char *session, const char *text);
void ND_Authenticated(const char *session, int role);
void ND_Monitor(const char *session, int index, int number, int width, int height, int primary, int selected);
void ND_ClearMonitors(const char *session);
void ND_Clipboard(const char *session, int enabled);
void ND_Control(const char *session, int isController, int localActive);
void ND_Approval(const char *session, const char *request, const char *identity);
void ND_CloseApproval(const char *session);
void ND_Close(const char *session);
void ND_Forget(const char *session);
void ND_Error(const char *text);
int ND_Frame(const char *session, const unsigned char *data, int length);
void ND_Panel(const char *session, const char *text, int members);
void ND_Laser(const char *session, const char *id, const char *name, const char *color, double x, double y);
void *ND_TestDecoderCreate(void);
int ND_TestDecoderFeed(void *, const unsigned char *, int, int *, int *);
void ND_TestDecoderClose(void *);
#ifdef __cplusplus
}
#endif
