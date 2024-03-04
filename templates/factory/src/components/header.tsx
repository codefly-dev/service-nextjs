import Link from "next/link";
import { ThemeSwitcher } from "./theme-switcher";

type HeaderProps = {
  
};

const Header = ({  }: HeaderProps) => {
  return (
    <div className="px-[50px] py-[25px] bg-white dark:bg-black border-b globals__border-color flex justify-between items-left gap-4 py-3">
      <div className="flex flex-direction-left">
      <Link href="/">
        <h1>Codefly</h1>
      </Link>

      <Link href="/demo" style={{ marginLeft: '20px'}}>
        <h1>Demo</h1>
      </Link>
      </div>

      <nav className="flex justify-center">
        <ul className="flex flex-wrap items-center justify-center gap-x-4 gap-y-2 text-[14px] [&_a:hover]:text-neutral-500 [&_a]:transition-all duration-200">      
          <li>
            <ThemeSwitcher />
          </li>
        </ul>
      </nav>
    </div>
  );
};

export default Header;
