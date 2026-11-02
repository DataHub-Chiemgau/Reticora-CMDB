import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button, type ButtonProps } from '../components/ui/Button';
import { signOut } from './signOut';

/**
 * SignOutButton ends the Reticora and the IdP session (signOut); the IdP
 * then returns to the sign-in page, where the next sign-in asks for
 * credentials. It stays busy while the browser leaves for the IdP.
 */
export function SignOutButton(props: Omit<ButtonProps, 'onClick' | 'loading'>) {
  const [signingOut, setSigningOut] = useState(false);
  return (
    <Button
      {...props}
      loading={signingOut}
      onClick={async () => {
        setSigningOut(true);
        if ((await signOut()) === 'local') {
          setSigningOut(false);
        }
      }}
    />
  );
}

/**
 * OtherAccountHint follows the creation of an account (self-signup,
 * invitation): an account still signed in at the IdP in this browser, e.g.
 * the superadmin's in another tab, would be signed in again instead of the
 * new one, so it offers that account's sign-out (F-351).
 */
export function OtherAccountHint() {
  const { t } = useTranslation();
  return (
    <div className="space-y-2 text-xs text-gray-600 dark:text-gray-400">
      <p>{t('auth.otherAccount.hint')}</p>
      <SignOutButton size="sm" variant="secondary">
        {t('auth.otherAccount.signOut')}
      </SignOutButton>
    </div>
  );
}
