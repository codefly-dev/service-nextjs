import Link from "next/link";
import { ThemeSwitcher } from "./theme-switcher";

type HeaderProps = {
  user?: any;
  loading: boolean;
};

const Header = ({ user, loading }: HeaderProps) => {
  return (
    <div className="px-[50px] bg-white dark:bg-black border-b globals__border-color flex justify-between items-center gap-4 py-3">
      <Link href="/">
        <h1>Next.js with Auth0</h1>
      </Link>

      <nav className="flex justify-center">
        <ul className="flex flex-wrap items-center justify-center gap-x-4 gap-y-2 text-[14px] [&_a:hover]:text-neutral-500 [&_a]:transition-all duration-200">
          {!loading &&
            (user ? (
              <>
                <li>
                  <Link href="/profile">
                    <span className="flex items-center justify-center leading-0 text-black w-8 h-8 rounded-full bg-gradient-to-b from-[#F3C37B] to-[#93F37B]">
                      {(user.name as string).trim().charAt(0)}
                    </span>
                  </Link>
                </li>

                <li>
                  <a
                    href="/api/auth/logout"
                    className="text-red-500 hover:!text-red-500"
                  >
                    <button>Logout</button>
                  </a>
                </li>
              </>
            ) : (
              <li>
                <a href="/api/auth/login">
                  <button>Login</button>
                </a>
              </li>
            ))}

          <li>
            <ThemeSwitcher />
          </li>
        </ul>
      </nav>
    </div>
  );
};

export default Header;
