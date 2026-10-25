
export interface DialogOpenFileRequest {
  title?: string;
  filter?: string;
  ext?: string;
}

export interface DialogOpenDirectoryRequest {
  title?: string;
  dir?: string;
}

export interface DialogSaveFileRequest {
  title?: string;
  filter?: string;
  ext?: string;
  fileName?: string;
  dir?: string;
}

export interface DialogMessageRequest {
  title?: string;
  text?: string;
  level?: string;
}

export interface DialogOpenFileDataRequest {
  title?: string;
  filter?: string;
  ext?: string;
}

export interface DialogFileData {
  name: string;
  data: string;
}

export interface DialogSaveFileDataRequest {
  title?: string;
  filter?: string;
  ext?: string;
  fileName?: string;
  data?: string;
}

export interface DialogOpenFolderRequest {
  title?: string;
}

export interface DialogFolder {
  id: string;
  name: string;
}

export interface DialogWriteFolderFileRequest {
  folder?: string;
  name?: string;
  ext?: string;
  data?: string;
}

export interface DialogPickFileRequest {
  title?: string;
  fileName?: string;
  ext?: string;
}

export interface DialogSavedFile {
  id: string;
  name: string;
}

export interface DialogRewriteFileRequest {
  file?: string;
  data?: string;
}

export const dialog = {
  openFile: (params?: DialogOpenFileRequest) => scorix.invoke<string>("mod:dialog:OpenFile", params),
  openDirectory: (params?: DialogOpenDirectoryRequest) => scorix.invoke<string>("mod:dialog:OpenDirectory", params),
  saveFile: (params?: DialogSaveFileRequest) => scorix.invoke<string>("mod:dialog:SaveFile", params),
  message: (params?: DialogMessageRequest) => scorix.invoke<string>("mod:dialog:Message", params),
  openFileData: (params?: DialogOpenFileDataRequest) => scorix.invoke<DialogFileData | null>("mod:dialog:OpenFileData", params),
  saveFileData: (params?: DialogSaveFileDataRequest) => scorix.invoke<string>("mod:dialog:SaveFileData", params),
  openFolder: (params?: DialogOpenFolderRequest) => scorix.invoke<DialogFolder | null>("mod:dialog:OpenFolder", params),
  writeFolderFile: (params?: DialogWriteFolderFileRequest) => scorix.invoke<string>("mod:dialog:WriteFolderFile", params),
  pickFile: (params?: DialogPickFileRequest) => scorix.invoke<DialogSavedFile | null>("mod:dialog:PickFile", params),
  rewriteFile: (params?: DialogRewriteFileRequest) => scorix.invoke<string>("mod:dialog:RewriteFile", params),
};
