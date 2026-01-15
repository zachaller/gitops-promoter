import React from 'react';
import { useToast } from './ToastContext';
import { ToastNotification } from './ToastNotification';
import './Toast.scss';

export const ToastContainer: React.FC = () => {
  const { toasts } = useToast();

  return (
    <div className="toast-container">
      {toasts.map((toast) => (
        <ToastNotification key={toast.id} toast={toast} />
      ))}
    </div>
  );
};
